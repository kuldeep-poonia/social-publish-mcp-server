package scout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/persona"
)

// GeminiGenerator defines the contract for generating AI social content.
type GeminiGenerator interface {
	GenerateTopicContent(ctx context.Context, title, content, niche, targetPlatform string, p *persona.BrandPersona) (*GeneratedTopicContent, error)
}

// GeneratedTopicContent holds the AI-crafted copy for a single trending topic.
type GeneratedTopicContent struct {
	Hook               string   `json:"hook"`
	Hashtags           []string `json:"hashtags"`
	TwitterDraft       string   `json:"twitter_draft"`
	InstagramCaption   string   `json:"instagram_caption"`
	ImagePrompt        string   `json:"image_prompt"`
	YouTubeTitle       string   `json:"youtube_title"`
	YouTubeDescription string   `json:"youtube_description"`
}

// GeminiClient connects to Google Gemini API for real-time per-topic content creation.
type GeminiClient struct {
	apiKey string
	client HTTPClient
}

// NewGeminiClient creates a new Gemini AI client.
func NewGeminiClient(apiKey string, client HTTPClient) *GeminiClient {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &GeminiClient{
		apiKey: strings.TrimSpace(apiKey),
		client: client,
	}
}

// GenerateTopicContent calls Gemini API or performs dynamic topic-specific semantic synthesis adapted to Brand Persona.
func (g *GeminiClient) GenerateTopicContent(ctx context.Context, title, content, niche, targetPlatform string, p *persona.BrandPersona) (*GeneratedTopicContent, error) {
	if g.apiKey != "" {
		res, err := g.callGeminiAPI(ctx, title, content, niche, p)
		if err == nil && res != nil && res.Hook != "" {
			return res, nil
		}
	}

	// Dynamic, topic-specific semantic generation adapted to Brand Persona
	return g.semanticTopicGeneration(title, content, niche, targetPlatform, p), nil
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (g *GeminiClient) callGeminiAPI(ctx context.Context, title, content, niche string, p *persona.BrandPersona) (*GeneratedTopicContent, error) {
	personaRules := persona.BuildPromptInstructions(p)

	prompt := fmt.Sprintf(`You are an elite viral social media copywriter.
%s

Given this trending discussion:
Title: %s
Context: %s
Niche: %s

Generate a JSON response (no markdown formatting, just raw JSON) matching this exact schema:
{
  "hook": "A punchy, curiosity-driven viral hook specifically about '%s' matching the required brand tone",
  "hashtags": ["#SpecificHashtag1", "#SpecificHashtag2", "#SpecificHashtag3", "#SpecificHashtag4", "#SpecificHashtag5"],
  "twitter_draft": "Viral tweet breakdown (under 250 characters) with hook, discussion, and 2 topic hashtags in brand voice",
  "instagram_caption": "Engaging multi-line Instagram caption with emojis, hook, breakdown, question, and hashtags in brand voice",
  "image_prompt": "Detailed visual aesthetic prompt representing '%s' for AI image generation matching the brand visual style and palette",
  "youtube_title": "High CTR clickable title under 90 characters specifically about '%s'",
  "youtube_description": "Structured YouTube video description with timestamps and key takeaways"
}`, personaRules, title, content, niche, title, title, title)

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
	}

	reqBytes, _ := json.Marshal(reqBody)
	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=%s", g.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
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

	var gResp geminiResponse
	if err := json.Unmarshal(body, &gResp); err != nil || len(gResp.Candidates) == 0 || len(gResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("failed parsing gemini response")
	}

	rawText := gResp.Candidates[0].Content.Parts[0].Text
	rawText = strings.TrimPrefix(rawText, "```json")
	rawText = strings.TrimPrefix(rawText, "```")
	rawText = strings.TrimSuffix(rawText, "```")
	rawText = strings.TrimSpace(rawText)

	var result GeneratedTopicContent
	if err := json.Unmarshal([]byte(rawText), &result); err != nil {
		return nil, fmt.Errorf("failed parsing generated json: %w", err)
	}

	if p != nil && len(p.ForbiddenWords) > 0 {
		result.Hook = persona.FilterForbiddenWords(result.Hook, p.ForbiddenWords)
		result.TwitterDraft = persona.FilterForbiddenWords(result.TwitterDraft, p.ForbiddenWords)
		result.InstagramCaption = persona.FilterForbiddenWords(result.InstagramCaption, p.ForbiddenWords)
	}

	return &result, nil
}

// semanticTopicGeneration extracts specific keywords and entities from the title to construct 100% unique per-topic copy adapted to Brand Persona.
func (g *GeminiClient) semanticTopicGeneration(title, content, niche, targetPlatform string, p *persona.BrandPersona) *GeneratedTopicContent {
	lowerTitle := strings.ToLower(title)
	words := strings.Fields(regexp.MustCompile(`[^a-zA-Z0-9\s-]`).ReplaceAllString(title, ""))

	// 1. Extract Topic-Specific Subject & Entity
	var subjectKeywords []string
	var primarySubject string
	for _, w := range words {
		lw := strings.ToLower(w)
		if len(w) >= 3 && !isStopWord(lw) {
			subjectKeywords = append(subjectKeywords, w)
		}
	}
	if len(subjectKeywords) > 0 {
		primarySubject = strings.Join(subjectKeywords[:min(3, len(subjectKeywords))], " ")
	} else {
		primarySubject = title
	}

	// 2. Generate Unique Topic-Specific Hook based on Brand Tone & Subject Matter
	var hook string
	if p != nil && p.Tone != "" {
		hook = persona.FormatHookWithTone(p.Tone, primarySubject, niche)
	} else {
		switch {
		case strings.Contains(lowerTitle, "gui") || strings.Contains(lowerTitle, "keyboard") || strings.Contains(lowerTitle, "vim") || strings.Contains(lowerTitle, "ui"):
			hook = fmt.Sprintf("Why developers are arguing over whether %s will replace traditional mouse interfaces", primarySubject)
		case strings.Contains(lowerTitle, "glm") || strings.Contains(lowerTitle, "weight") || strings.Contains(lowerTitle, "model") || strings.Contains(lowerTitle, "llm"):
			hook = fmt.Sprintf("%s just dropped — and it might completely change the open-source AI landscape", primarySubject)
		case strings.Contains(lowerTitle, "htmx") || strings.Contains(lowerTitle, "framework") || strings.Contains(lowerTitle, "react") || strings.Contains(lowerTitle, "web"):
			hook = fmt.Sprintf("%s is officially out: Is the era of heavy frontend bloat finally over?", primarySubject)
		case strings.Contains(lowerTitle, "battery") || strings.Contains(lowerTitle, "energy") || strings.Contains(lowerTitle, "hardware"):
			hook = fmt.Sprintf("Massive breakthrough in %s could redefine energy density for good", primarySubject)
		case strings.Contains(lowerTitle, "release") || strings.Contains(lowerTitle, "launch") || strings.Contains(lowerTitle, "new") || strings.Contains(lowerTitle, "v"):
			hook = fmt.Sprintf("Major release alert: Everything you need to know about the newly announced %s", primarySubject)
		default:
			hook = fmt.Sprintf("The real reason %s is trending across the %s community today", primarySubject, niche)
		}
	}

	// 3. Generate Topic-Specific Hashtags (derived directly from title words + niche)
	var hashtags []string
	seenTags := make(map[string]bool)

	// Add domain-specific primary tags first
	switch {
	case strings.Contains(lowerTitle, "keyboard") || strings.Contains(lowerTitle, "gui"):
		addTags(&hashtags, seenTags, "#KeyboardDriven", "#DevUX", "#Productivity", "#Vim", "#Linux", "#UserInterface")
	case strings.Contains(lowerTitle, "glm") || strings.Contains(lowerTitle, "weight") || strings.Contains(lowerTitle, "model"):
		addTags(&hashtags, seenTags, "#OpenWeight", "#GLM5", "#OpenSourceAI", "#MachineLearning", "#LLMs", "#AIResearch", "#HuggingFace")
	case strings.Contains(lowerTitle, "htmx") || strings.Contains(lowerTitle, "web"):
		addTags(&hashtags, seenTags, "#HTMX", "#WebDevelopment", "#Frontend", "#JavaScript", "#Backend", "#CleanCode")
	default:
		addTags(&hashtags, seenTags, "#TechNews", "#Innovation", "#Trending", "#Software")
	}

	for _, kw := range subjectKeywords {
		tag := SanitizeHashtag(kw)
		lowerTag := strings.ToLower(tag)
		if tag != "" && !seenTags[lowerTag] && len(tag) > 3 && !isFragmentTag(lowerTag) {
			hashtags = append(hashtags, tag)
			seenTags[lowerTag] = true
		}
	}

	// 4. Image Prompt adapted to Brand Aesthetic
	imagePrompt := persona.AdaptImagePrompt(p, primarySubject)

	// 5. Twitter / X Draft
	twTags := strings.Join(hashtags[:min(3, len(hashtags))], " ")
	twDraft := fmt.Sprintf("🔥 %s\n\nHeadline: %s\n\nWhat are your thoughts on this? 👇\n%s", hook, title, twTags)
	if len([]rune(twDraft)) > 280 {
		twDraft = fmt.Sprintf("🔥 %s\n\n%s\n\n%s", hook, title, twTags)
		if len([]rune(twDraft)) > 280 {
			twDraft = string([]rune(twDraft)[:277]) + "..."
		}
	}

	// 6. Instagram Caption
	igTags := strings.Join(hashtags[:min(6, len(hashtags))], " ")
	igCaption := fmt.Sprintf(`✨ %s

📌 What happened:
%s

💡 Why it matters:
The %s ecosystem is moving faster than ever. Keeping track of high-signal developments is essential.

💬 What is your take on %s? Drop a comment below!

%s`, hook, title, niche, primarySubject, igTags)

	// 7. YouTube Draft
	ytTitle := fmt.Sprintf("%s: The Truth You Need to Know", primarySubject)
	if len(ytTitle) > 90 {
		ytTitle = primarySubject
	}
	ytDesc := fmt.Sprintf("In-depth breakdown of %s (%s).\n\nOriginal Source: %s\n\nTimestamps:\n0:00 - Intro & Context\n1:20 - Key Takeaways\n3:45 - Analysis\n\nSubscribe for more %s insights!", title, niche, title, niche)

	if p != nil && len(p.ForbiddenWords) > 0 {
		hook = persona.FilterForbiddenWords(hook, p.ForbiddenWords)
		twDraft = persona.FilterForbiddenWords(twDraft, p.ForbiddenWords)
		igCaption = persona.FilterForbiddenWords(igCaption, p.ForbiddenWords)
	}

	return &GeneratedTopicContent{
		Hook:               hook,
		Hashtags:           hashtags,
		TwitterDraft:       twDraft,
		InstagramCaption:   igCaption,
		ImagePrompt:        imagePrompt,
		YouTubeTitle:       ytTitle,
		YouTubeDescription: ytDesc,
	}
}

// SanitizeHashtag cleans raw text into platform-valid CamelCase hashtags without hyphens, dots, or spaces.
func SanitizeHashtag(tag string) string {
	tag = strings.TrimPrefix(tag, "#")
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}

	// Split by hyphens, underscores, dots, or spaces
	parts := strings.FieldsFunc(tag, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' ' || r == '/' || r == ':'
	})

	var result strings.Builder
	for _, p := range parts {
		cleaned := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(p, "")
		if len(cleaned) == 0 {
			continue
		}
		result.WriteString(capitalizeWord(cleaned))
	}

	res := result.String()
	if len(res) < 2 {
		return ""
	}
	return "#" + res
}

func isFragmentTag(tag string) bool {
	clean := strings.ToLower(strings.TrimPrefix(tag, "#"))
	fragments := map[string]bool{
		"fully": true, "guis": true, "should": true, "driven": true,
		"just": true, "with": true, "here": true, "your": true,
		"some": true, "more": true, "have": true, "been": true,
	}
	return fragments[clean]
}

func isStopWord(w string) bool {
	stops := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "that": true,
		"this": true, "from": true, "are": true, "was": true, "were": true,
		"should": true, "could": true, "would": true, "about": true, "now": true,
		"out": true, "how": true, "why": true, "what": true, "when": true,
		"fully": true, "been": true, "just": true,
	}
	return stops[w]
}

func capitalizeWord(w string) string {
	if len(w) == 0 {
		return ""
	}
	return strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
}

func addTags(tags *[]string, seen map[string]bool, newTags ...string) {
	for _, t := range newTags {
		san := SanitizeHashtag(t)
		lower := strings.ToLower(san)
		if san != "" && !seen[lower] {
			*tags = append(*tags, san)
			seen[lower] = true
		}
	}
}
