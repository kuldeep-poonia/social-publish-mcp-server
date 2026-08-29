// Package scout provides real-time trending topic scanning and automated platform draft generation.
package scout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

// ScoutRequest defines options for scanning trends.
type ScoutRequest struct {
	UserID     string `json:"user_id"`
	Niche      string `json:"niche"`
	Platform   string `json:"platform,omitempty"`   // "twitter", "instagram", "youtube", or "all"
	Limit      int    `json:"limit,omitempty"`      // Number of trends to return (default: 5, max: 20)
	AutoDraft  bool   `json:"auto_draft,omitempty"` // Whether to auto-generate platform drafts
	SaveDrafts bool   `json:"save_drafts,omitempty"`// Whether to persist drafts in database
}

// PlatformDraft represents a generated draft for a specific social network.
type PlatformDraft struct {
	Platform    string   `json:"platform"`
	Content     string   `json:"content"`
	Title       string   `json:"title,omitempty"`
	Hashtags    []string `json:"hashtags,omitempty"`
	ImagePrompt string   `json:"image_prompt,omitempty"`
	DraftPostID string   `json:"draft_post_id,omitempty"` // Populated if SaveDrafts is true
}

// TrendingTopic represents an analyzed real-world topic with live metrics.
type TrendingTopic struct {
	Topic                   string                   `json:"topic"`
	Niche                   string                   `json:"niche"`
	Source                  string                   `json:"source"`
	SourceURL               string                   `json:"source_url"`
	LiveUpvotes             int                      `json:"live_upvotes"`
	LiveComments            int                      `json:"live_comments"`
	ViralityScore           int                      `json:"virality_score"` // 1-100 mathematical score
	Momentum                string                   `json:"momentum"`       // "exploding", "rising", "steady"
	VelocityPointsPerHour   float64                  `json:"velocity_pts_per_hr"`
	ViralHookAngle          string                   `json:"viral_hook_angle"`
	SuggestedHashtags       []string                 `json:"suggested_hashtags"`
	Drafts                  map[string]PlatformDraft `json:"drafts,omitempty"`
}

// ScoutReport represents the complete response from a scout query.
type ScoutReport struct {
	ScoutedAt   time.Time       `json:"scouted_at"`
	Niche       string          `json:"niche"`
	TotalFound  int             `json:"total_found"`
	Topics      []TrendingTopic `json:"topics"`
	SavedDrafts int             `json:"saved_drafts_count,omitempty"`
}

// Service orchestrates trending topic ingestion and draft synthesis.
type Service struct {
	db             *sql.DB
	repo           *database.Repository
	redditIngestor Ingestor
	hnIngestor     Ingestor
	geminiGen      GeminiGenerator
}

// NewService initializes a new Scout Service.
func NewService(db *sql.DB, repo *database.Repository, redditIngestor, hnIngestor Ingestor, geminiGen GeminiGenerator) *Service {
	if redditIngestor == nil {
		redditIngestor = NewRedditIngestor(nil)
	}
	if hnIngestor == nil {
		hnIngestor = NewHackerNewsIngestor(nil)
	}
	if geminiGen == nil {
		geminiGen = NewGeminiClient("", nil)
	}
	return &Service{
		db:             db,
		repo:           repo,
		redditIngestor: redditIngestor,
		hnIngestor:     hnIngestor,
		geminiGen:      geminiGen,
	}
}

// ScoutTrendingTopics queries real public sources, calculates engagement velocity, and constructs drafts.
func (s *Service) ScoutTrendingTopics(ctx context.Context, req *ScoutRequest) (*ScoutReport, error) {
	if req == nil {
		return nil, errors.New("scout request cannot be nil")
	}

	actor := database.GetActor(ctx)
	if actor.ActorID != "" && actor.ActorID != "anonymous" && req.UserID != "" && actor.ActorID != req.UserID {
		return nil, database.ErrUnauthorizedAccess
	}
	if req.UserID == "" && actor.ActorID != "" && actor.ActorID != "anonymous" {
		req.UserID = actor.ActorID
	}

	niche := strings.TrimSpace(req.Niche)
	if niche == "" {
		niche = "ai_tech"
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	now := time.Now().UTC()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var rawItems []RawTrendItem
	var redditErr, hnErr error

	// Concurrently query Reddit and Hacker News
	wg.Add(2)
	go func() {
		defer wg.Done()
		items, err := s.redditIngestor.FetchTrends(ctx, niche, limit)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			redditErr = err
		} else {
			rawItems = append(rawItems, items...)
		}
	}()

	go func() {
		defer wg.Done()
		items, err := s.hnIngestor.FetchTrends(ctx, niche, limit)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			hnErr = err
		} else {
			rawItems = append(rawItems, items...)
		}
	}()

	wg.Wait()

	// Handle upstream outage / rate limiting
	if len(rawItems) == 0 {
		if errors.Is(redditErr, ErrRateLimited) || errors.Is(hnErr, ErrRateLimited) {
			return nil, ErrRateLimited
		}
		if redditErr != nil && hnErr != nil {
			return nil, fmt.Errorf("trending data sources unavailable (Reddit: %v | HN: %v)", redditErr, hnErr)
		}
		return nil, ErrNoTrendsFound
	}

	// Calculate velocity scores and sort by descending momentum
	type scoredItem struct {
		item     RawTrendItem
		velocity float64
		score    int
		momentum string
	}

	var scored []scoredItem
	seenTitles := make(map[string]bool)

	for _, it := range rawItems {
		normTitle := strings.ToLower(strings.TrimSpace(it.Title))
		if seenTitles[normTitle] || len(normTitle) < 5 {
			continue
		}
		seenTitles[normTitle] = true

		vel, score, momentum := it.CalculateVelocity(now)
		scored = append(scored, scoredItem{
			item:     it,
			velocity: vel,
			score:    score,
			momentum: momentum,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].velocity > scored[j].velocity
	})

	if len(scored) > limit {
		scored = scored[:limit]
	}

	targetPlatform := strings.ToLower(strings.TrimSpace(req.Platform))
	if targetPlatform == "" {
		targetPlatform = "all"
	}

	var topics []TrendingTopic
	var savedDraftsCount int

	for _, sc := range scored {
		it := sc.item
		aiContent, err := s.geminiGen.GenerateTopicContent(ctx, it.Title, it.Content, niche, targetPlatform)
		if err != nil || aiContent == nil {
			aiContent = &GeneratedTopicContent{
				Hook:               s.generateHookAngle(it.Title, niche),
				Hashtags:           s.generateHashtags(it.Title, niche),
				TwitterDraft:       fmt.Sprintf("🔥 %s\n\n%s", it.Title, it.SourceURL),
				InstagramCaption:   fmt.Sprintf("✨ %s\n\n%s", it.Title, it.SourceURL),
				ImagePrompt:        fmt.Sprintf("Editorial render for %s", it.Title),
				YouTubeTitle:       it.Title,
				YouTubeDescription: fmt.Sprintf("Deep dive into %s", it.Title),
			}
		}

		topic := TrendingTopic{
			Topic:                 it.Title,
			Niche:                 niche,
			Source:                it.Source,
			SourceURL:             it.SourceURL,
			LiveUpvotes:           it.Upvotes,
			LiveComments:          it.Comments,
			ViralityScore:         sc.score,
			Momentum:              sc.momentum,
			VelocityPointsPerHour: math.Round(sc.velocity*10) / 10,
			ViralHookAngle:        aiContent.Hook,
			SuggestedHashtags:     aiContent.Hashtags,
			Drafts:                make(map[string]PlatformDraft),
		}

		if req.AutoDraft {
			drafts := make(map[string]PlatformDraft)

			// 1. Twitter Draft
			if targetPlatform == "all" || targetPlatform == "twitter" {
				drafts["twitter"] = PlatformDraft{
					Platform: "twitter",
					Content:  aiContent.TwitterDraft,
					Hashtags: aiContent.Hashtags[:min(3, len(aiContent.Hashtags))],
				}
			}

			// 2. Instagram Draft
			if targetPlatform == "all" || targetPlatform == "instagram" {
				drafts["instagram"] = PlatformDraft{
					Platform:    "instagram",
					Content:     aiContent.InstagramCaption,
					Hashtags:    aiContent.Hashtags,
					ImagePrompt: aiContent.ImagePrompt,
				}
			}

			// 3. YouTube Shorts/Video Draft
			if targetPlatform == "all" || targetPlatform == "youtube" {
				drafts["youtube"] = PlatformDraft{
					Platform: "youtube",
					Title:    aiContent.YouTubeTitle,
					Content:  aiContent.YouTubeDescription,
					Hashtags: aiContent.Hashtags,
				}
			}

			if req.SaveDrafts && s.db != nil && req.UserID != "" {
				for _, pName := range []string{"twitter", "instagram", "youtube"} {
					d, ok := drafts[pName]
					if !ok {
						continue
					}
					draftID, err := s.saveDraftToDB(ctx, req.UserID, pName, d.Content, d.Title, d.ImagePrompt, d.Hashtags, it.SourceURL)
					if err == nil {
						d.DraftPostID = draftID
						drafts[pName] = d
						savedDraftsCount++
					} else {
						log.Printf("[Scout] Warning: failed saving draft for %s: %v", pName, err)
					}
				}
			}
			topic.Drafts = drafts
		}

		topics = append(topics, topic)
	}

	return &ScoutReport{
		ScoutedAt:   now,
		Niche:       niche,
		TotalFound:  len(topics),
		Topics:      topics,
		SavedDrafts: savedDraftsCount,
	}, nil
}

// synthesizeDrafts formats tailored posts across Twitter, Instagram, and YouTube.
func (s *Service) synthesizeDrafts(title, hook string, hashtags []string, niche, targetPlatform string) map[string]PlatformDraft {
	drafts := make(map[string]PlatformDraft)
	tagStr := strings.Join(hashtags[:min(3, len(hashtags))], " ")

	// 1. Twitter / X Draft (<= 280 chars)
	if targetPlatform == "all" || targetPlatform == "twitter" {
		twText := fmt.Sprintf("🔥 %s\n\n%s\n\nWhat are your thoughts on this? 👇\n%s", hook, title, tagStr)
		if len([]rune(twText)) > 280 {
			twText = fmt.Sprintf("🔥 %s\n\n%s\n\n%s", hook, title, tagStr)
			if len([]rune(twText)) > 280 {
				twText = string([]rune(twText)[:277]) + "..."
			}
		}
		drafts["twitter"] = PlatformDraft{
			Platform: "twitter",
			Content:  twText,
			Hashtags: hashtags[:min(3, len(hashtags))],
		}
	}

	// 2. Instagram Draft (Visual Prompt + Captivating Caption + 15 Hashtag Cluster)
	if targetPlatform == "all" || targetPlatform == "instagram" {
		igTags := strings.Join(hashtags, " ")
		igCaption := fmt.Sprintf("✨ %s\n\n📌 Breakdown: %s\n\nSwipe through for the key insights! Drop your perspective in the comments below 👇\n.\n.\n%s", hook, title, igTags)
		imgPrompt := fmt.Sprintf("A striking, high-detail editorial aesthetic concept visual representing '%s', futuristic cinematic lighting, 8k resolution, minimalist modern design", title)

		drafts["instagram"] = PlatformDraft{
			Platform:    "instagram",
			Content:     igCaption,
			Hashtags:    hashtags,
			ImagePrompt: imgPrompt,
		}
	}

	// 3. YouTube Shorts / Video Draft (High-CTR Title <= 100 chars + Structured Description)
	if targetPlatform == "all" || targetPlatform == "youtube" {
		ytTitle := fmt.Sprintf("%s (Why This Changes Everything)", title)
		if len([]rune(ytTitle)) > 95 {
			ytTitle = title
			if len([]rune(ytTitle)) > 95 {
				ytTitle = string([]rune(ytTitle)[:92]) + "..."
			}
		}

		ytDesc := fmt.Sprintf("🚀 In this video, we break down the latest breakthrough: %s\n\n🔥 Core Hook: %s\n\n⏱️ Key Takeaways:\n0:00 - The Big Picture\n0:45 - Key Developments\n1:30 - Future Outlook\n\n💬 Subscribe for daily %s insights!\n\nTags: %s",
			title, hook, niche, strings.Join(hashtags, ", "))

		drafts["youtube"] = PlatformDraft{
			Platform: "youtube",
			Title:    ytTitle,
			Content:  ytDesc,
			Hashtags: hashtags,
		}
	}

	return drafts
}

func (s *Service) generateHookAngle(title, niche string) string {
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "why") || strings.Contains(lower, "how"):
		return "The secret behind this major shift in the industry"
	case strings.Contains(lower, "release") || strings.Contains(lower, "launch") || strings.Contains(lower, "new"):
		return "Just dropped: The biggest update everyone is talking about"
	case strings.Contains(lower, "ai") || strings.Contains(lower, "model") || strings.Contains(lower, "gpt"):
		return "AI just took another massive leap forward"
	case strings.Contains(lower, "free") || strings.Contains(lower, "open source"):
		return "This game-changing tool is completely open-source"
	default:
		return fmt.Sprintf("Why this trending %s development matters right now", niche)
	}
}

func (s *Service) generateHashtags(title, niche string) []string {
	base := []string{"#Trending", "#Viral", "#Innovation"}
	nicheTags := map[string][]string{
		"ai_tech":           {"#AI", "#ArtificialIntelligence", "#TechNews", "#MachineLearning", "#FutureTech", "#DeepLearning", "#OpenAI", "#Robotics"},
		"crypto":            {"#Crypto", "#Bitcoin", "#Ethereum", "#Web3", "#Blockchain", "#DeFi", "#CryptoNews"},
		"business_startups": {"#Startup", "#Entrepreneur", "#BusinessGrowth", "#Founder", "#SaaS", "#Productivity", "#Leadership"},
		"programming":       {"#Coding", "#Developer", "#SoftwareEngineering", "#WebDev", "#Programmer", "#TechCommunity", "#CodeNewbie"},
		"fitness":           {"#Fitness", "#Health", "#Workout", "#Nutrition", "#Bodybuilding", "#FitnessMotivation"},
		"gaming":            {"#Gaming", "#Gamers", "#GamingCommunity", "#PCGaming", "#GameDev"},
	}

	if tags, exists := nicheTags[niche]; exists {
		return append(tags, base...)
	}
	return append(nicheTags["ai_tech"], base...)
}

func (s *Service) saveDraftToDB(ctx context.Context, userID, platform, content, title, imagePrompt string, hashtags []string, sourceURL string) (string, error) {
	draftID := uuid.New().String()
	now := time.Now().UTC()

	meta := map[string]interface{}{
		"source_url": sourceURL,
		"hashtags":   hashtags,
		"is_draft":   true,
	}
	if title != "" {
		meta["title"] = title
	}
	metaJSON, _ := json.Marshal(meta)

	query := `
		INSERT INTO posts (
			id, user_id, platform, content, media_urls, media_path, media_type,
			image_prompt, status, scheduled_at, idempotency_key, metadata, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, 'draft', NULL, $9, $10, $11, $12
		) RETURNING id;
	`
	var id string
	idempKey := fmt.Sprintf("draft-%s-%s", draftID, platform)
	err := s.db.QueryRowContext(
		ctx, query,
		draftID, userID, platform, content,
		models.StringArray{}, "", "IMAGE",
		imagePrompt, idempKey, metaJSON, now, now,
	).Scan(&id)

	if err != nil {
		return "", fmt.Errorf("failed saving draft: %w", err)
	}

	return id, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
