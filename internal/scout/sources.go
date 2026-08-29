// Package scout provides real-time trending topic ingestion from public feeds and automated multi-platform draft generation.
package scout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrRateLimited is returned when upstream public APIs return 429.
	ErrRateLimited = errors.New("trending data temporarily unavailable due to upstream rate limiting, please retry in 5 minutes")
	// ErrUpstreamUnavailable is returned when upstream APIs are unreachable or return 5xx.
	ErrUpstreamUnavailable = errors.New("trending data sources temporarily unreachable")
	// ErrNoTrendsFound is returned when no active discussions match the requested criteria.
	ErrNoTrendsFound = errors.New("no trending discussions found for the specified niche")
)

// RedditUserAgent adheres to Reddit's API guidelines: '<platform>:<app ID>:<version string> (by /u/<reddit username>)'
const RedditUserAgent = "web:social-publish-mcp-server:v1.0.0 (by /u/social_publisher_mcp)"

// RawTrendItem represents a normalized trending item parsed from live public feeds.
type RawTrendItem struct {
	Source      string    `json:"source"`       // "reddit", "hackernews", "github"
	SourceURL   string    `json:"source_url"`   // Direct link to discussion
	Title       string    `json:"title"`        // Headline
	Content     string    `json:"content"`      // Body or summary text
	Upvotes     int       `json:"upvotes"`      // Upvotes / Points / Stars
	Comments    int       `json:"comments"`     // Comment count
	PublishedAt time.Time `json:"published_at"` // Timestamp when post was created
	Subreddit   string    `json:"subreddit,omitempty"`
}

// CalculateVelocity computes mathematical engagement velocity: (Upvotes + 2*Comments) / (Hours + 1).
func (item *RawTrendItem) CalculateVelocity(now time.Time) (velocity float64, score int, momentum string) {
	ageHours := now.Sub(item.PublishedAt).Hours()
	if ageHours < 0 {
		ageHours = 0
	}

	rawEngagement := float64(item.Upvotes) + (2.0 * float64(item.Comments))
	velocity = rawEngagement / (ageHours + 1.0)

	// Determine virality score and momentum based on velocity
	switch {
	case velocity >= 50.0:
		momentum = "exploding"
		// Scale between 85 and 99
		scaled := 85 + int(math.Min(14, (velocity-50.0)/10.0))
		score = scaled
	case velocity >= 20.0:
		momentum = "rising"
		// Scale between 65 and 84
		scaled := 65 + int(math.Min(19, (velocity-20.0)/1.5))
		score = scaled
	default:
		momentum = "steady"
		// Scale between 40 and 64
		scaled := 40 + int(math.Min(24, velocity/1.0))
		score = scaled
	}

	if score > 99 {
		score = 99
	}
	if score < 1 {
		score = 1
	}

	return velocity, score, momentum
}

// Ingestor defines the contract for fetching real public trends.
type Ingestor interface {
	FetchTrends(ctx context.Context, niche string, limit int) ([]RawTrendItem, error)
}

// HTTPClient interface for testability and timeout management.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// RedditIngestor scrapes public Reddit JSON endpoints with strict compliant User-Agent headers.
type RedditIngestor struct {
	client    HTTPClient
	userAgent string
}

// NewRedditIngestor initializes a new RedditIngestor.
func NewRedditIngestor(client HTTPClient) *RedditIngestor {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &RedditIngestor{
		client:    client,
		userAgent: RedditUserAgent,
	}
}

// NicheSubreddits maps popular niches to active high-signal subreddits.
var NicheSubreddits = map[string][]string{
	"ai_tech":           {"artificial", "MachineLearning", "technology", "OpenAI", "singularity"},
	"tech":              {"technology", "gadgets", "futurology", "programming"},
	"crypto":            {"CryptoCurrency", "Bitcoin", "ethereum"},
	"business_startups": {"entrepreneur", "startups", "SideProject", "smallbusiness"},
	"programming":       {"programming", "webdev", "golang", "learnprogramming"},
	"fitness":           {"fitness", "bodyweightfitness", "nutrition"},
	"gaming":            {"gaming", "pcgaming", "Games"},
	"finance":           {"personalfinance", "investing", "financialindependence"},
}

// ResolveSubreddits selects subreddits matching the user's requested niche.
func ResolveSubreddits(niche string) []string {
	clean := strings.ToLower(strings.TrimSpace(niche))
	clean = strings.ReplaceAll(clean, " ", "_")
	clean = strings.ReplaceAll(clean, "&", "")
	clean = strings.ReplaceAll(clean, "__", "_")

	for k, subs := range NicheSubreddits {
		if strings.Contains(clean, k) || strings.Contains(k, clean) {
			return subs
		}
	}

	if strings.Contains(clean, "ai") || strings.Contains(clean, "gpt") || strings.Contains(clean, "ml") {
		return NicheSubreddits["ai_tech"]
	}
	if strings.Contains(clean, "code") || strings.Contains(clean, "dev") || strings.Contains(clean, "software") {
		return NicheSubreddits["programming"]
	}
	if strings.Contains(clean, "money") || strings.Contains(clean, "stock") || strings.Contains(clean, "market") {
		return NicheSubreddits["finance"]
	}
	if strings.Contains(clean, "biz") || strings.Contains(clean, "founder") || strings.Contains(clean, "saas") {
		return NicheSubreddits["business_startups"]
	}

	// Default fallback to tech and futurology
	return []string{"technology", "futurology"}
}

type redditListing struct {
	Data struct {
		Children []struct {
			Data struct {
				Title       string  `json:"title"`
				Selftext    string  `json:"selftext"`
				Score       int     `json:"score"`
				Ups         int     `json:"ups"`
				NumComments int     `json:"num_comments"`
				Permalink   string  `json:"permalink"`
				URL         string  `json:"url"`
				CreatedUTC  float64 `json:"created_utc"`
				Over18      bool    `json:"over_18"`
				Stickied    bool    `json:"stickied"`
				Subreddit   string  `json:"subreddit"`
			} `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

// FetchTrends retrieves hot discussion posts from Reddit for the target niche.
func (r *RedditIngestor) FetchTrends(ctx context.Context, niche string, limit int) ([]RawTrendItem, error) {
	subreddits := ResolveSubreddits(niche)
	if limit <= 0 {
		limit = 10
	}

	var allItems []RawTrendItem
	var rateLimitHit bool

	for _, sub := range subreddits {
		if len(allItems) >= limit*2 {
			break
		}

		endpoints := []string{
			fmt.Sprintf("https://www.reddit.com/r/%s/hot.json?limit=%d", sub, limit),
			fmt.Sprintf("https://old.reddit.com/r/%s/hot.json?limit=%d", sub, limit),
			fmt.Sprintf("https://www.reddit.com/r/%s/top.json?t=day&limit=%d", sub, limit),
		}

		var bodyBytes []byte
		for _, apiURL := range endpoints {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
			if err != nil {
				continue
			}

			// Strict Reddit Compliant User-Agent
			req.Header.Set("User-Agent", r.userAgent)
			req.Header.Set("Accept", "application/json")

			resp, err := r.client.Do(req)
			if err != nil {
				continue
			}

			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
				resp.Body.Close()
				rateLimitHit = true
				continue
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				continue
			}

			data, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil && len(data) > 0 {
				bodyBytes = data
				rateLimitHit = false
				break
			}
		}

		if len(bodyBytes) == 0 {
			continue
		}

		var listing redditListing
		if err := json.Unmarshal(bodyBytes, &listing); err != nil {
			continue
		}

		for _, child := range listing.Data.Children {
			d := child.Data
			if d.Stickied || d.Over18 || strings.TrimSpace(d.Title) == "" {
				continue
			}

			upvotes := d.Ups
			if upvotes <= 0 {
				upvotes = d.Score
			}

			publishedAt := time.Unix(int64(d.CreatedUTC), 0).UTC()
			sourceURL := d.URL
			if sourceURL == "" || !strings.HasPrefix(sourceURL, "http") {
				sourceURL = "https://www.reddit.com" + d.Permalink
			}

			allItems = append(allItems, RawTrendItem{
				Source:      "reddit",
				SourceURL:   sourceURL,
				Title:       strings.TrimSpace(d.Title),
				Content:     strings.TrimSpace(d.Selftext),
				Upvotes:     upvotes,
				Comments:    d.NumComments,
				PublishedAt: publishedAt,
				Subreddit:   "r/" + d.Subreddit,
			})
		}
	}

	if len(allItems) == 0 && rateLimitHit {
		return nil, ErrRateLimited
	}

	return allItems, nil
}

// HackerNewsIngestor queries Hacker News official Algolia & Firebase public endpoints.
type HackerNewsIngestor struct {
	client HTTPClient
}

// NewHackerNewsIngestor initializes a new HackerNewsIngestor.
func NewHackerNewsIngestor(client HTTPClient) *HackerNewsIngestor {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &HackerNewsIngestor{client: client}
}

type hnAlgoliaResponse struct {
	Hits []struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Points      int    `json:"points"`
		NumComments int    `json:"num_comments"`
		CreatedAtI  int64  `json:"created_at_i"`
		ObjectID    string `json:"objectID"`
		StoryText   string `json:"story_text"`
	} `json:"hits"`
}

// FetchTrends retrieves top trending stories from Hacker News.
func (h *HackerNewsIngestor) FetchTrends(ctx context.Context, niche string, limit int) ([]RawTrendItem, error) {
	if limit <= 0 {
		limit = 10
	}

	apiURL := fmt.Sprintf("https://hn.algolia.com/api/v1/search?tags=front_page&hitsPerPage=%d", limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SocialPublishMCPServer/1.0")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hackernews request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hackernews returned status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var hnResp hnAlgoliaResponse
	if err := json.Unmarshal(bodyBytes, &hnResp); err != nil {
		return nil, err
	}

	var items []RawTrendItem
	for _, hit := range hnResp.Hits {
		if strings.TrimSpace(hit.Title) == "" {
			continue
		}

		sourceURL := hit.URL
		if sourceURL == "" {
			sourceURL = fmt.Sprintf("https://news.ycombinator.com/item?id=%s", hit.ObjectID)
		}

		items = append(items, RawTrendItem{
			Source:      "hackernews",
			SourceURL:   sourceURL,
			Title:       strings.TrimSpace(hit.Title),
			Content:     strings.TrimSpace(hit.StoryText),
			Upvotes:     hit.Points,
			Comments:    hit.NumComments,
			PublishedAt: time.Unix(hit.CreatedAtI, 0).UTC(),
			Subreddit:   "news.ycombinator.com",
		})
	}

	return items, nil
}
