package scout

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
)

type mockHTTPClient struct {
	doFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.doFunc(req)
}

func TestCalculateVelocity_Thresholds(t *testing.T) {
	now := time.Now().UTC()

	// 1. Fresh post with high engagement (Velocity > 50 -> Exploding)
	explodingItem := RawTrendItem{
		Upvotes:     300,
		Comments:    100,
		PublishedAt: now.Add(-1 * time.Hour), // 1 hour old: (300 + 200) / 2 = 250 pts/hr
	}
	vel, score, momentum := explodingItem.CalculateVelocity(now)
	if momentum != "exploding" {
		t.Errorf("expected momentum 'exploding', got '%s' (vel: %f)", momentum, vel)
	}
	if score < 85 || score > 99 {
		t.Errorf("expected score 85-99 for exploding, got %d", score)
	}

	// 2. Medium engagement (Velocity 20-50 -> Rising)
	risingItem := RawTrendItem{
		Upvotes:     50,
		Comments:    20,
		PublishedAt: now.Add(-2 * time.Hour), // 2 hours old: (50 + 40) / 3 = 30 pts/hr
	}
	vel, score, momentum = risingItem.CalculateVelocity(now)
	if momentum != "rising" {
		t.Errorf("expected momentum 'rising', got '%s' (vel: %f)", momentum, vel)
	}
	if score < 65 || score > 84 {
		t.Errorf("expected score 65-84 for rising, got %d", score)
	}

	// 3. Low / older engagement (Velocity < 20 -> Steady)
	steadyItem := RawTrendItem{
		Upvotes:     20,
		Comments:    5,
		PublishedAt: now.Add(-5 * time.Hour), // 5 hours old: (20 + 10) / 6 = 5 pts/hr
	}
	vel, score, momentum = steadyItem.CalculateVelocity(now)
	if momentum != "steady" {
		t.Errorf("expected momentum 'steady', got '%s' (vel: %f)", momentum, vel)
	}
	if score < 40 || score > 64 {
		t.Errorf("expected score 40-64 for steady, got %d", score)
	}
}

func TestRedditIngestor_StrictUserAgentAndRateLimit(t *testing.T) {
	var capturedUA string
	mockClient := &mockHTTPClient{
		doFunc: func(req *http.Request) (*http.Response, error) {
			capturedUA = req.Header.Get("User-Agent")
			if strings.Contains(req.URL.Path, "ratelimited") {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader("429 Too Many Requests")),
				}, nil
			}

			// Valid Reddit JSON response
			sampleJSON := `{
				"data": {
					"children": [
						{
							"data": {
								"title": "OpenAI Releases New Frontier Model",
								"selftext": "Details about the latest release...",
								"ups": 1500,
								"num_comments": 450,
								"permalink": "/r/OpenAI/comments/12345/openai_release/",
								"url": "https://openai.com/blog/new-model",
								"created_utc": 1700000000,
								"stickied": false,
								"over_18": false,
								"subreddit": "OpenAI"
							}
						}
					]
				}
			}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(sampleJSON)),
			}, nil
		},
	}

	ingestor := NewRedditIngestor(mockClient)
	ctx := context.Background()

	items, err := ingestor.FetchTrends(ctx, "ai_tech", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify compliant User-Agent
	if capturedUA != RedditUserAgent {
		t.Errorf("expected User-Agent '%s', got '%s'", RedditUserAgent, capturedUA)
	}

	if len(items) == 0 {
		t.Fatalf("expected items to be parsed from JSON")
	}
	if items[0].Title != "OpenAI Releases New Frontier Model" {
		t.Errorf("unexpected title: %s", items[0].Title)
	}
	if items[0].Upvotes != 1500 || items[0].Comments != 450 {
		t.Errorf("unexpected metrics: upvotes=%d, comments=%d", items[0].Upvotes, items[0].Comments)
	}
}

type staticIngestor struct {
	items []RawTrendItem
	err   error
}

func (s *staticIngestor) FetchTrends(ctx context.Context, niche string, limit int) ([]RawTrendItem, error) {
	return s.items, s.err
}

func TestScoutService_EndToEndDraftSynthesisAndPersistence(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed initializing sqlmock: %v", err)
	}
	defer db.Close()

	testUserID := uuid.New().String()
	now := time.Now().UTC()

	mockReddit := &staticIngestor{
		items: []RawTrendItem{
			{
				Source:      "reddit",
				SourceURL:   "https://reddit.com/r/technology/comments/abc",
				Title:       "Revolutionary Next-Gen Battery Breakthrough Announced",
				Content:     "Researchers have achieved 10x energy density in lab tests.",
				Upvotes:     850,
				Comments:    210,
				PublishedAt: now.Add(-1 * time.Hour),
				Subreddit:   "r/technology",
			},
		},
	}

	mockHN := &staticIngestor{
		items: []RawTrendItem{
			{
				Source:      "hackernews",
				SourceURL:   "https://news.ycombinator.com/item?id=999",
				Title:       "Show HN: Open Source Autonomous Social Agent",
				Content:     "A self-hosted social media automation agent.",
				Upvotes:     420,
				Comments:    180,
				PublishedAt: now.Add(-2 * time.Hour),
				Subreddit:   "news.ycombinator.com",
			},
		},
	}

	svc := NewService(db, nil, mockReddit, mockHN)
	ctx := database.WithActor(context.Background(), database.ActorContext{ActorID: testUserID})

	// Set expectation for SaveDrafts DB insertions (1 topic * 3 platforms = 3 drafts)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO posts")).
		WithArgs(
			sqlmock.AnyArg(), testUserID, "twitter", sqlmock.AnyArg(),
			sqlmock.AnyArg(), "", "IMAGE",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.New().String()))

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO posts")).
		WithArgs(
			sqlmock.AnyArg(), testUserID, "instagram", sqlmock.AnyArg(),
			sqlmock.AnyArg(), "", "IMAGE",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.New().String()))

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO posts")).
		WithArgs(
			sqlmock.AnyArg(), testUserID, "youtube", sqlmock.AnyArg(),
			sqlmock.AnyArg(), "", "IMAGE",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.New().String()))

	report, err := svc.ScoutTrendingTopics(ctx, &ScoutRequest{
		UserID:     testUserID,
		Niche:      "ai_tech",
		Platform:   "all",
		Limit:      1,
		AutoDraft:  true,
		SaveDrafts: true,
	})

	if err != nil {
		t.Fatalf("ScoutTrendingTopics failed: %v", err)
	}

	if report.TotalFound != 1 {
		t.Fatalf("expected 1 topic found, got %d", report.TotalFound)
	}

	top := report.Topics[0]
	if top.ViralityScore < 85 || top.Momentum != "exploding" {
		t.Errorf("expected exploding momentum, got %s (score: %d)", top.Momentum, top.ViralityScore)
	}

	// Verify drafts generated
	twDraft, hasTw := top.Drafts["twitter"]
	if !hasTw || len(twDraft.Content) > 280 {
		t.Errorf("invalid Twitter draft: %+v (len=%d)", twDraft, len(twDraft.Content))
	}

	igDraft, hasIg := top.Drafts["instagram"]
	if !hasIg || igDraft.ImagePrompt == "" || len(igDraft.Hashtags) == 0 {
		t.Errorf("invalid Instagram draft: %+v", igDraft)
	}

	ytDraft, hasYt := top.Drafts["youtube"]
	if !hasYt || ytDraft.Title == "" || len(ytDraft.Title) > 100 {
		t.Errorf("invalid YouTube draft: %+v", ytDraft)
	}

	if report.SavedDrafts != 3 {
		t.Errorf("expected 3 saved drafts in database, got %d", report.SavedDrafts)
	}
}

func TestScoutService_RateLimitGracefulHandling(t *testing.T) {
	mockReddit := &staticIngestor{err: ErrRateLimited}
	mockHN := &staticIngestor{err: ErrRateLimited}

	svc := NewService(nil, nil, mockReddit, mockHN)
	_, err := svc.ScoutTrendingTopics(context.Background(), &ScoutRequest{Niche: "ai_tech"})

	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited when both upstream sources rate limit, got: %v", err)
	}
}
