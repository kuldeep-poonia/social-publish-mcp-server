package optimizer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/youtube"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/scout"
)

// UpdateMetadataRequest holds input arguments for optimizing and updating post metadata.
type UpdateMetadataRequest struct {
	UserID            string   `json:"user_id"`
	PostID            string   `json:"post_id"` // Database post UUID OR Platform external Video/Post ID
	Platform          string   `json:"platform"`
	Objective         string   `json:"objective"` // "ctr_boost", "seo_search", "viral_rehook", "retention"
	Niche             string   `json:"niche"`
	TargetAudience    string   `json:"target_audience"`
	CustomTitle       string   `json:"custom_title,omitempty"`
	CustomDescription string   `json:"custom_description,omitempty"`
	CustomTags        []string `json:"custom_tags,omitempty"`
	AutoOptimizeAI    bool     `json:"auto_optimize_ai"`
	ApplyLive         bool     `json:"apply_live"`
}

// MetadataOptimizationReport returns the optimization outcomes and live update confirmation.
type MetadataOptimizationReport struct {
	PostID                 string                          `json:"post_id"`
	Platform               string                          `json:"platform"`
	PreviousTitle          string                          `json:"previous_title,omitempty"`
	PreviousDescription    string                          `json:"previous_description,omitempty"`
	OptimizedTitle         string                          `json:"optimized_title"`
	TitleCTRVariations     []string                        `json:"title_ctr_variations"`
	OptimizedDescription   string                          `json:"optimized_description"`
	OptimizedTags          []string                        `json:"optimized_tags"`
	PredictedImpact        string                          `json:"predicted_impact"`
	OptimizationApplied    bool                            `json:"optimization_applied"`
	LiveVideoAnalytics     *youtube.VideoAnalyticsMetrics `json:"live_video_analytics,omitempty"`
	DatabaseRecordUpdated  bool                            `json:"database_record_updated"`
	OptimizedAt            time.Time                       `json:"optimized_at"`
}

// Service orchestrates metadata optimization and live platform updates.
type Service struct {
	db            *sql.DB
	repo          *database.Repository
	youtubeClient *youtube.Client
	geminiKey     string
	httpClient    scout.HTTPClient
}

// NewService initializes a new Metadata Optimizer Service.
func NewService(db *sql.DB, repo *database.Repository, ytClient *youtube.Client, geminiKey string, httpClient scout.HTTPClient) *Service {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Service{
		db:            db,
		repo:          repo,
		youtubeClient: ytClient,
		geminiKey:     strings.TrimSpace(geminiKey),
		httpClient:    httpClient,
	}
}

// UpdatePostMetadata generates CTR-optimized copy and pushes updates live to platform & database.
func (s *Service) UpdatePostMetadata(ctx context.Context, req *UpdateMetadataRequest) (*MetadataOptimizationReport, error) {
	if req.PostID == "" {
		return nil, errors.New("post_id is required for metadata update")
	}
	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	if req.Platform == "" {
		req.Platform = "youtube"
	}
	if req.Objective == "" {
		req.Objective = "ctr_boost"
	}

	var previousTitle, previousDesc string
	var existingTags []string

	// 1. Check if PostID is in local PostgreSQL database
	if s.db != nil && req.UserID != "" && s.isUUID(req.PostID) {
		row := s.db.QueryRowContext(ctx, "SELECT content, metadata FROM posts WHERE id = $1 AND user_id = $2", req.PostID, req.UserID)
		var content sql.NullString
		var metadata []byte
		if err := row.Scan(&content, &metadata); err == nil {
			previousDesc = content.String
			if len(metadata) > 0 {
				var metaMap map[string]interface{}
				if err := json.Unmarshal(metadata, &metaMap); err == nil {
					if t, ok := metaMap["title"].(string); ok {
						previousTitle = t
					}
				}
			}
		}
	}

	// 2. Perform AI / Algorithmic CTR Optimization
	optResult := s.synthesizeOptimizedMetadata(ctx, req, previousTitle, previousDesc, existingTags)

	report := &MetadataOptimizationReport{
		PostID:                req.PostID,
		Platform:              req.Platform,
		PreviousTitle:         previousTitle,
		PreviousDescription:   previousDesc,
		OptimizedTitle:        optResult.PrimaryTitle,
		TitleCTRVariations:    optResult.TitleVariations,
		OptimizedDescription:  optResult.Description,
		OptimizedTags:         optResult.Tags,
		PredictedImpact:       optResult.PredictedImpact,
		OptimizationApplied:   false,
		DatabaseRecordUpdated: false,
		OptimizedAt:           time.Now().UTC(),
	}

	// 3. Apply Live Updates if requested
	if req.ApplyLive {
		switch req.Platform {
		case "youtube":
			if s.youtubeClient != nil && s.repo != nil && req.UserID != "" {
				accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, req.UserID, "youtube")
				if err != nil {
					return nil, fmt.Errorf("failed retrieving YouTube credentials for live update: %w", err)
				}

				ytParams := &youtube.UpdateVideoMetadataParams{
					VideoID:     req.PostID,
					Title:       optResult.PrimaryTitle,
					Description: optResult.Description,
					Tags:        optResult.Tags,
				}

				metrics, err := s.youtubeClient.UpdateVideoMetadata(ctx, string(accessBytes), ytParams)
				if err != nil && len(refreshBytes) > 0 {
					// Attempt token refresh on auth failure
					if newTok, refErr := s.youtubeClient.RefreshToken(ctx, string(refreshBytes)); refErr == nil {
						_ = s.repo.SavePlatformConnection(ctx, req.UserID, "youtube", []byte(newTok.AccessToken), []byte(newTok.RefreshToken), time.Now().Add(time.Duration(newTok.ExpiresIn)*time.Second), scopes)
						metrics, err = s.youtubeClient.UpdateVideoMetadata(ctx, newTok.AccessToken, ytParams)
					}
				}
				if err != nil {
					return nil, fmt.Errorf("failed applying live YouTube metadata update: %w", err)
				}

				report.OptimizationApplied = true
				report.LiveVideoAnalytics = metrics
			}

		case "instagram", "twitter":
			// Live metadata optimization applied to database record for published / scheduled posts
			report.OptimizationApplied = true
		}
	}

	// 4. Update Database record if exists
	if s.db != nil && s.isUUID(req.PostID) {
		metadataJSON, _ := json.Marshal(map[string]interface{}{
			"title":                 optResult.PrimaryTitle,
			"title_variations":      optResult.TitleVariations,
			"tags":                  optResult.Tags,
			"optimization_objective": req.Objective,
			"last_optimized_at":     time.Now().UTC(),
		})

		query := `
			UPDATE posts 
			SET content = $1, metadata = $2, updated_at = $3
			WHERE id = $4 AND user_id = $5
		`
		res, err := s.db.ExecContext(ctx, query, optResult.Description, metadataJSON, time.Now().UTC(), req.PostID, req.UserID)
		if err == nil {
			if count, _ := res.RowsAffected(); count > 0 {
				report.DatabaseRecordUpdated = true
			}
		}
	}

	return report, nil
}

type aiOptimizationOutput struct {
	PrimaryTitle    string
	TitleVariations []string
	Description     string
	Tags            []string
	PredictedImpact string
}

func (s *Service) synthesizeOptimizedMetadata(ctx context.Context, req *UpdateMetadataRequest, prevTitle, prevDesc string, prevTags []string) *aiOptimizationOutput {
	baseTitle := req.CustomTitle
	if baseTitle == "" {
		baseTitle = prevTitle
	}
	if baseTitle == "" || s.isIDLike(baseTitle) {
		// If baseTitle is missing or looks like an ID/UUID, extract topic from prevDesc if available
		if prevDesc != "" {
			firstLine := strings.TrimSpace(strings.Split(prevDesc, "\n")[0])
			firstLine = strings.TrimLeft(firstLine, "✨🔥🚀📌•-—*# ")
			if len(firstLine) > 5 && !s.isIDLike(firstLine) {
				baseTitle = firstLine
			}
		}
	}
	if baseTitle == "" || s.isIDLike(baseTitle) {
		if req.Niche != "" {
			cleanNiche := strings.ReplaceAll(req.Niche, "_", " ")
			baseTitle = capitalizeWords(cleanNiche) + " Optimization"
		} else {
			baseTitle = "Creator Content Strategy"
		}
	}

	baseDesc := req.CustomDescription
	if baseDesc == "" {
		baseDesc = prevDesc
	}

	// Try live Gemini API if key is present
	if s.geminiKey != "" {
		res, err := s.callGeminiOptimization(ctx, req, baseTitle, baseDesc)
		if err == nil && res != nil && res.PrimaryTitle != "" {
			return res
		}
	}

	// Dynamic CTR Formula Synthesis with clean subject extraction
	cleanSubject := cleanEntityTitle(baseTitle)
	if cleanSubject == "" || s.isIDLike(cleanSubject) {
		cleanSubject = "Modern Workflow"
	}

	// 3 Distinct High-Converting Angles (Natural phrasing, zero double-year repetition)
	variations := []string{
		fmt.Sprintf("Why %s is the Future of Tech (Complete Breakdown)", cleanSubject),
		fmt.Sprintf("The Truth About %s Nobody is Talking About", cleanSubject),
		fmt.Sprintf("Mastering %s: Everything You Need to Know", cleanSubject),
	}

	primaryTitle := variations[0]
	if req.CustomTitle != "" {
		primaryTitle = req.CustomTitle
	}

	// Generate sanitized, platform-valid keyword tags
	var tags []string
	seenTags := make(map[string]bool)
	for _, t := range req.CustomTags {
		san := scout.SanitizeHashtag(t)
		clean := strings.TrimPrefix(san, "#")
		if clean != "" && !seenTags[strings.ToLower(clean)] && !s.isIDLike(clean) {
			tags = append(tags, clean)
			seenTags[strings.ToLower(clean)] = true
		}
	}
	if len(tags) == 0 {
		tagWords := strings.Fields(cleanSubject)
		for _, w := range tagWords {
			san := scout.SanitizeHashtag(w)
			clean := strings.TrimPrefix(san, "#")
			if len(clean) > 2 && !seenTags[strings.ToLower(clean)] && !s.isIDLike(clean) {
				tags = append(tags, clean)
				seenTags[strings.ToLower(clean)] = true
			}
		}
		if req.Niche != "" {
			sanNiche := strings.TrimPrefix(scout.SanitizeHashtag(req.Niche), "#")
			if !seenTags[strings.ToLower(sanNiche)] {
				tags = append([]string{sanNiche}, tags...)
				seenTags[strings.ToLower(sanNiche)] = true
			}
		}
		for _, t := range []string{"Technology", "Innovation", "Tutorial", "Productivity", "DeepDive"} {
			if !seenTags[strings.ToLower(t)] {
				tags = append(tags, t)
				seenTags[strings.ToLower(t)] = true
			}
		}
	}

	tagLine := "#" + strings.Join(tags[:min(5, len(tags))], " #")
	optimizedDesc := fmt.Sprintf(`🚀 Complete deep dive: %s.

🔥 Core Hook: We break down the exact mechanics, benchmarks, and real-world takeaways so you can stay ahead.

⏱️ Timestamps:
0:00 - The Big Picture & Context
1:15 - Deep Dive Analysis
3:45 - Key Takeaways & Pro Tips
5:30 - Future Outlook

💬 Drop your thoughts and questions in the comments below!
🔔 Subscribe for weekly high-signal %s breakdowns.

%s`, cleanSubject, req.Niche, tagLine)

	if req.CustomDescription != "" {
		optimizedDesc = req.CustomDescription
	}

	return &aiOptimizationOutput{
		PrimaryTitle:    primaryTitle,
		TitleVariations: variations,
		Description:     optimizedDesc,
		Tags:            tags,
		PredictedImpact: "Optimization Strategy: Curiosity-gap title rehooking & high-intent search discovery keywords (Unverified estimate, not measured)",
	}
}

func (s *Service) callGeminiOptimization(ctx context.Context, req *UpdateMetadataRequest, title, desc string) (*aiOptimizationOutput, error) {
	prompt := fmt.Sprintf(`You are a world-class YouTube & Social Media CTR Optimization specialist.
Optimize this post metadata for maximum Click-Through Rate (CTR), search discovery, and audience retention.

Current Title: %s
Current Description: %s
Target Platform: %s
Objective: %s
Niche: %s
Target Audience: %s

Respond with raw JSON matching this exact structure (do NOT use markdown fences or code blocks):
{
  "primary_title": "Best high-CTR title under 90 chars using curiosity gap or value hook (fresh rewrite, no repetitive templates)",
  "title_variations": [
    "Angle 1: Curiosity / Open Loop title",
    "Angle 2: Contrarian / Critical perspective title",
    "Angle 3: Step-by-step Value / Case study title"
  ],
  "description": "Structured high-retention description with timestamps, key takeaway bullets, and calls to action",
  "tags": ["Keyword1", "Keyword2", "Keyword3", "Keyword4", "Keyword5"],
  "predicted_impact": "Optimization Strategy: Qualitative rationale for the changes without inventing fake percentage statistics"
}`, title, desc, req.Platform, req.Objective, req.Niche, req.TargetAudience)

	reqPayload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]string{{"text": prompt}}},
		},
	}
	reqBytes, _ := json.Marshal(reqPayload)
	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=%s", s.geminiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var gResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &gResp); err != nil || len(gResp.Candidates) == 0 || len(gResp.Candidates[0].Content.Parts) == 0 {
		return nil, errors.New("invalid gemini response format")
	}

	rawText := gResp.Candidates[0].Content.Parts[0].Text
	rawText = strings.TrimPrefix(rawText, "```json")
	rawText = strings.TrimPrefix(rawText, "```")
	rawText = strings.TrimSuffix(rawText, "```")
	rawText = strings.TrimSpace(rawText)

	var res struct {
		PrimaryTitle    string   `json:"primary_title"`
		TitleVariations []string `json:"title_variations"`
		Description     string   `json:"description"`
		Tags            []string `json:"tags"`
		PredictedImpact string   `json:"predicted_impact"`
	}
	if err := json.Unmarshal([]byte(rawText), &res); err != nil {
		return nil, err
	}

	return &aiOptimizationOutput{
		PrimaryTitle:    res.PrimaryTitle,
		TitleVariations: res.TitleVariations,
		Description:     res.Description,
		Tags:            res.Tags,
		PredictedImpact: res.PredictedImpact,
	}, nil
}

func cleanEntityTitle(t string) string {
	// Strip year markers to prevent double year repetition
	t = regexp.MustCompile(`(?i)\b(202[0-9]|2030)\b`).ReplaceAllString(t, " ")
	t = regexp.MustCompile(`(?i)\b(why|how|what|is|now|official|released|the|a|an|in|for|are|taking|over|tested|days|changes|everything)\b`).ReplaceAllString(t, " ")
	t = regexp.MustCompile(`[^a-zA-Z0-9\s-]`).ReplaceAllString(t, " ")
	words := strings.Fields(t)
	var filtered []string
	for _, w := range words {
		if len(w) >= 3 && !isHexOrHash(w) {
			filtered = append(filtered, capitalizeWord(w))
		}
	}
	if len(filtered) == 0 {
		return "Modern Tech Workflow"
	}
	return strings.Join(filtered[:min(4, len(filtered))], " ")
}

func (s *Service) isUUID(u string) bool {
	_, err := uuid.Parse(u)
	return err == nil
}

func (s *Service) isIDLike(str string) bool {
	str = strings.TrimSpace(str)
	if _, err := uuid.Parse(str); err == nil {
		return true
	}
	if matched, _ := regexp.MatchString(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, str); matched {
		return true
	}
	if matched, _ := regexp.MatchString(`(?i)^(yt_|tw_|ig_)?[0-9a-f_-]{16,}$`, str); matched {
		return true
	}
	return false
}

func isHexOrHash(s string) bool {
	if len(s) >= 8 && regexp.MustCompile(`^[0-9a-fA-F-]+$`).MatchString(s) {
		return true
	}
	return false
}

func capitalizeWord(w string) string {
	if len(w) == 0 {
		return ""
	}
	return strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
}

func capitalizeWords(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = capitalizeWord(w)
	}
	return strings.Join(words, " ")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

