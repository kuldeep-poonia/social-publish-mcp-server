// Package queue provides background worker pool processing with exponential backoff and poison message mitigation.
package queue

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"time"
)

// JobHandler executes a publish job against the target platform.
type JobHandler func(ctx context.Context, job *PublishJob) error

// WorkerPool manages concurrent queue consumer workers.
type WorkerPool struct {
	queue         *RedisStreamQueue
	handler       JobHandler
	policy        RetryPolicy
	numWorkers    int
	workerName    string
	claimInterval time.Duration
	claimMinIdle  time.Duration
	stopCh        chan struct{}
	wg            sync.WaitGroup
	mu            sync.Mutex
	running       bool
}

// NewWorkerPool initializes a background queue worker pool.
func NewWorkerPool(queue *RedisStreamQueue, handler JobHandler, policy RetryPolicy, numWorkers int, workerName string) *WorkerPool {
	if numWorkers <= 0 {
		numWorkers = 4
	}
	if workerName == "" {
		workerName = "social_mcp_worker"
	}
	if policy.MaxDeliveries <= 0 {
		policy.MaxDeliveries = 5
	}
	if policy.MaxRetries <= 0 {
		policy.MaxRetries = 5
	}
	if policy.BaseBackoff <= 0 {
		policy.BaseBackoff = 500 * time.Millisecond
	}
	if policy.MaxBackoff <= 0 {
		policy.MaxBackoff = 30 * time.Second
	}

	return &WorkerPool{
		queue:         queue,
		handler:       handler,
		policy:        policy,
		numWorkers:    numWorkers,
		workerName:    workerName,
		claimInterval: 5 * time.Second,
		claimMinIdle:  30 * time.Second,
		stopCh:        make(chan struct{}),
	}
}

// Start launches worker goroutines and the abandoned pending message reclaim sweeper.
func (p *WorkerPool) Start(ctx context.Context) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	// Launch consumer worker goroutines
	for i := 0; i < p.numWorkers; i++ {
		consumerID := fmt.Sprintf("%s_%d", p.workerName, i)
		p.wg.Add(1)
		go p.workerLoop(ctx, consumerID)
	}

	// Launch periodic abandoned message reclaim sweeper
	p.wg.Add(1)
	go p.claimSweeperLoop(ctx)
}

// Stop gracefully stops all workers and waits for current jobs to finish.
func (p *WorkerPool) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	close(p.stopCh)
	p.mu.Unlock()

	p.wg.Wait()
}

func (p *WorkerPool) workerLoop(ctx context.Context, consumerID string) {
	defer p.wg.Done()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ctx.Done():
			return
		default:
			// Read up to 5 unread messages with 1s block
			messages, err := p.queue.ReadGroup(ctx, consumerID, 5, 1*time.Second)
			if err != nil || len(messages) == 0 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			for _, msg := range messages {
				p.processMessage(ctx, msg)
			}
		}
	}
}

func (p *WorkerPool) claimSweeperLoop(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.claimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Claim abandoned pending messages from crashed workers
			claimed, err := p.queue.ClaimPending(ctx, fmt.Sprintf("%s_reclaim", p.workerName), p.claimMinIdle, 10)
			if err != nil || len(claimed) == 0 {
				continue
			}

			for _, msg := range claimed {
				p.processMessage(ctx, msg)
			}
		}
	}
}

// ProcessSingleMessage exposes message processing for deterministic testing.
func (p *WorkerPool) ProcessSingleMessage(ctx context.Context, msg *StreamMessage) {
	p.processMessage(ctx, msg)
}

func (p *WorkerPool) processMessage(ctx context.Context, msg *StreamMessage) {
	if msg == nil || msg.Job == nil {
		return
	}

	job := msg.Job

	// 1. Poison Message Mitigation Guard: Delivery Count Check
	if msg.Deliveries >= int64(p.policy.MaxDeliveries) {
		_ = p.queue.Acknowledge(ctx, msg.StreamID)
		_ = p.queue.EnqueueDeadLetter(ctx, &DeadLetterRecord{
			JobID:           job.ID,
			PostID:          job.PostID,
			UserID:          job.UserID,
			Platform:        job.Platform,
			TotalAttempts:   job.AttemptCount,
			FirstAttemptAt:  job.CreatedAt,
			FailedAt:        time.Now().UTC(),
			ErrorCategory:   ErrorCategoryPoisonMessageDeliveryCap,
			DiagnosticTrace: fmt.Sprintf("poison message detected: message exceeded %d worker deliveries without completion", p.policy.MaxDeliveries),
		})
		return
	}

	// 2. Execute Job Handler
	err := p.handler(ctx, job)
	if err == nil {
		// Job succeeded: acknowledge and remove from stream
		_ = p.queue.Acknowledge(ctx, msg.StreamID)
		return
	}

	// 3. Classify Failure
	isTransient, category := ClassifyError(err)
	job.LastErrorMessage = err.Error()
	job.LastErrorCategory = category

	if isTransient && job.AttemptCount < p.policy.MaxRetries {
		// Transient Error: Calculate Exponential Backoff + Jitter
		job.AttemptCount++
		delay := CalculateBackoff(job.AttemptCount, p.policy.BaseBackoff, p.policy.MaxBackoff)
		job.NextRunAt = time.Now().UTC().Add(delay)

		// Re-enqueue for delayed retry and acknowledge original message
		_ = p.queue.Acknowledge(ctx, msg.StreamID)
		_ = p.queue.Enqueue(ctx, job)
		return
	}

	// 4. Permanent Error or Retry Limit Exhausted -> Route to Dead-Letter Queue
	finalCategory := category
	if isTransient && job.AttemptCount >= p.policy.MaxRetries {
		finalCategory = ErrorCategoryRetryLimitExhausted
	}

	_ = p.queue.Acknowledge(ctx, msg.StreamID)
	_ = p.queue.EnqueueDeadLetter(ctx, &DeadLetterRecord{
		JobID:           job.ID,
		PostID:          job.PostID,
		UserID:          job.UserID,
		Platform:        job.Platform,
		TotalAttempts:   job.AttemptCount + 1,
		FirstAttemptAt:  job.CreatedAt,
		FailedAt:        time.Now().UTC(),
		ErrorCategory:   finalCategory,
		DiagnosticTrace: err.Error(),
	})
}

// CalculateBackoff computes exponential backoff with decorrelated full jitter.
func CalculateBackoff(attempt int, base, max time.Duration) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}

	// Multiplier = 2^(attempt-1)
	multiplier := math.Pow(2, float64(attempt-1))
	rawBackoff := float64(base) * multiplier

	if rawBackoff > float64(max) {
		rawBackoff = float64(max)
	}

	// Apply 25% randomized decorrelated jitter
	jitterRange := int64(rawBackoff * 0.25)
	if jitterRange <= 0 {
		jitterRange = 1
	}

	n, _ := rand.Int(rand.Reader, big.NewInt(jitterRange*2))
	jitterOffset := n.Int64() - jitterRange

	finalBackoff := time.Duration(rawBackoff) + time.Duration(jitterOffset)
	if finalBackoff < base {
		finalBackoff = base
	}
	return finalBackoff
}

// ClassifyError inspects error messages/types and categorizes them into transient vs permanent.
func ClassifyError(err error) (isTransient bool, category ErrorCategory) {
	if err == nil {
		return false, ""
	}

	msg := strings.ToLower(err.Error())

	// Transient Rate Limiting
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many requests") || strings.Contains(msg, "quota_exceeded") {
		return true, ErrorCategoryTransientRateLimit
	}

	// Transient Server Errors
	if strings.Contains(msg, "500") || strings.Contains(msg, "502") || strings.Contains(msg, "503") || strings.Contains(msg, "504") ||
		strings.Contains(msg, "service unavailable") || strings.Contains(msg, "bad gateway") || strings.Contains(msg, "internal server error") {
		return true, ErrorCategoryTransientServerError
	}

	// Transient Network / Socket / Timeout Errors
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "connection reset") || strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "connection refused") || strings.Contains(msg, "eof") || strings.Contains(msg, "broken pipe") {
		return true, ErrorCategoryTransientNetworkTimeout
	}

	// Permanent Errors (400 Bad Request, 401 Unauthorized, 403 Forbidden, Dimension mismatch, suspended)
	return false, ErrorCategoryPermanentClientError
}
