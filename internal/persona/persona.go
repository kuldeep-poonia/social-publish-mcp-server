package persona

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

// BrandPersona represents a locked brand tone, voice rules, visual aesthetic, and audience definition.
type BrandPersona struct {
	ID              string    `json:"id"`
	UserID          string    `json:"user_id"`
	BrandName       string    `json:"brand_name"`
	Tone            string    `json:"tone"`             // "sarcastic", "witty", "authoritative", "casual_chill", "bold_provocative", "academic", "inspirational"
	VisualStyle     string    `json:"visual_style"`     // "cyberpunk_dark_neon", "minimalist_studio", "retro_vintage", "clean_corporate", "bold_brutalist"
	ColorPalette    string    `json:"color_palette"`    // e.g. "#0F172A, #38BDF8, #818CF8", "Neon Green & Obsidian"
	VoiceGuidelines string    `json:"voice_guidelines"` // Rules for sentence length, rhetoric, personality
	ForbiddenWords  []string  `json:"forbidden_words"`  // Prohibited words (e.g. buzzwords)
	TargetAudience  string    `json:"target_audience"`
	IsDefault       bool      `json:"is_default"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// SetPersonaRequest holds input arguments to configure or update a brand persona.
type SetPersonaRequest struct {
	UserID          string   `json:"user_id"`
	BrandName       string   `json:"brand_name"`
	Tone            string   `json:"tone"`
	VisualStyle     string   `json:"visual_style"`
	ColorPalette    string   `json:"color_palette"`
	VoiceGuidelines string   `json:"voice_guidelines"`
	ForbiddenWords  []string `json:"forbidden_words"`
	TargetAudience  string   `json:"target_audience"`
}

// Service manages brand personas and runtime prompt/aesthetic policy enforcement.
type Service struct {
	db             *sql.DB
	mu             sync.RWMutex
	memoryPersonas map[string]*BrandPersona
}

// NewService initializes a new Persona Service.
func NewService(db *sql.DB) *Service {
	return &Service{
		db:             db,
		memoryPersonas: make(map[string]*BrandPersona),
	}
}

// SetBrandPersona creates or updates the default persona for a user.
func (s *Service) SetBrandPersona(ctx context.Context, req *SetPersonaRequest) (*BrandPersona, error) {
	if req.UserID == "" {
		return nil, errors.New("user_id is required")
	}
	if strings.TrimSpace(req.BrandName) == "" {
		req.BrandName = "Default Brand"
	}
	if strings.TrimSpace(req.Tone) == "" {
		req.Tone = "authoritative_inspirational"
	}
	if strings.TrimSpace(req.VisualStyle) == "" {
		req.VisualStyle = "modern_cinematic_minimalist"
	}
	if strings.TrimSpace(req.ColorPalette) == "" {
		req.ColorPalette = "#0F172A, #38BDF8, #818CF8"
	}
	if req.ForbiddenWords == nil {
		req.ForbiddenWords = []string{}
	}

	personaID := uuid.New().String()
	now := time.Now().UTC()

	if s.db != nil {
		query := `
			INSERT INTO brand_personas (
				id, user_id, brand_name, tone, visual_style, color_palette,
				voice_guidelines, forbidden_words, target_audience, is_default, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, TRUE, $10, $11
			)
			ON CONFLICT (user_id) WHERE is_default = TRUE
			DO UPDATE SET
				brand_name = EXCLUDED.brand_name,
				tone = EXCLUDED.tone,
				visual_style = EXCLUDED.visual_style,
				color_palette = EXCLUDED.color_palette,
				voice_guidelines = EXCLUDED.voice_guidelines,
				forbidden_words = EXCLUDED.forbidden_words,
				target_audience = EXCLUDED.target_audience,
				updated_at = EXCLUDED.updated_at
			RETURNING id, created_at, updated_at;
		`
		err := s.db.QueryRowContext(ctx, query,
			personaID, req.UserID, req.BrandName, req.Tone, req.VisualStyle, req.ColorPalette,
			req.VoiceGuidelines, models.StringArray(req.ForbiddenWords), req.TargetAudience, now, now,
		).Scan(&personaID, &now, &now)
		if err != nil {
			return nil, fmt.Errorf("failed storing brand persona: %w", err)
		}
	}

	p := &BrandPersona{
		ID:              personaID,
		UserID:          req.UserID,
		BrandName:       req.BrandName,
		Tone:            req.Tone,
		VisualStyle:     req.VisualStyle,
		ColorPalette:    req.ColorPalette,
		VoiceGuidelines: req.VoiceGuidelines,
		ForbiddenWords:  req.ForbiddenWords,
		TargetAudience:  req.TargetAudience,
		IsDefault:       true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	s.mu.Lock()
	if s.memoryPersonas == nil {
		s.memoryPersonas = make(map[string]*BrandPersona)
	}
	s.memoryPersonas[req.UserID] = p
	s.mu.Unlock()

	return p, nil
}

// GetBrandPersona retrieves the active brand persona for a user, or returns safe defaults.
func (s *Service) GetBrandPersona(ctx context.Context, userID string) (*BrandPersona, error) {
	if userID == "" {
		return s.DefaultPersona("anonymous"), nil
	}

	if s.db == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		if p, ok := s.memoryPersonas[userID]; ok && p != nil {
			return p, nil
		}
		return s.DefaultPersona(userID), nil
	}

	query := `
		SELECT id, user_id, brand_name, tone, visual_style, color_palette,
		       voice_guidelines, forbidden_words, target_audience, is_default, created_at, updated_at
		FROM brand_personas
		WHERE user_id = $1 AND is_default = TRUE;
	`
	row := s.db.QueryRowContext(ctx, query, userID)

	var p BrandPersona
	var forbidden models.StringArray
	err := row.Scan(
		&p.ID, &p.UserID, &p.BrandName, &p.Tone, &p.VisualStyle, &p.ColorPalette,
		&p.VoiceGuidelines, &forbidden, &p.TargetAudience, &p.IsDefault, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.mu.RLock()
			defer s.mu.RUnlock()
			if memP, ok := s.memoryPersonas[userID]; ok && memP != nil {
				return memP, nil
			}
			return s.DefaultPersona(userID), nil
		}
		return nil, fmt.Errorf("failed querying brand persona: %w", err)
	}
	p.ForbiddenWords = []string(forbidden)

	s.mu.Lock()
	if s.memoryPersonas == nil {
		s.memoryPersonas = make(map[string]*BrandPersona)
	}
	s.memoryPersonas[userID] = &p
	s.mu.Unlock()

	return &p, nil
}

// DefaultPersona returns a standardized high-converting brand persona if none is configured.
func (s *Service) DefaultPersona(userID string) *BrandPersona {
	return &BrandPersona{
		ID:              "default_persona",
		UserID:          userID,
		BrandName:       "Creator Brand",
		Tone:            "authoritative_inspirational",
		VisualStyle:     "modern_cinematic_minimalist",
		ColorPalette:    "#0F172A, #38BDF8, #818CF8",
		VoiceGuidelines: "Clear, high-signal, engaging, focused on actionable insights without fluff.",
		ForbiddenWords:  []string{"synergy", "paradigm", "delve", "tapestry", "plethora"},
		TargetAudience:  "Tech innovators, developers, and digital creators",
		IsDefault:       true,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
}

// BuildPromptInstructions generates structured LLM system prompts enforcing tone and guidelines.
func BuildPromptInstructions(p *BrandPersona) string {
	if p == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n=== BRAND PERSONA & VOICE CONSTRAINTS ===\n")
	sb.WriteString(fmt.Sprintf("Brand Name: %s\n", p.BrandName))
	sb.WriteString(fmt.Sprintf("Required Tone: %s\n", strings.ToUpper(p.Tone)))
	sb.WriteString(fmt.Sprintf("Visual Aesthetic / Palette: %s (Colors: %s)\n", p.VisualStyle, p.ColorPalette))
	if p.TargetAudience != "" {
		sb.WriteString(fmt.Sprintf("Target Audience: %s\n", p.TargetAudience))
	}
	if p.VoiceGuidelines != "" {
		sb.WriteString(fmt.Sprintf("Voice Rules: %s\n", p.VoiceGuidelines))
	}
	if len(p.ForbiddenWords) > 0 {
		sb.WriteString(fmt.Sprintf("STRICT PROHIBITED WORDS (NEVER USE): %s\n", strings.Join(p.ForbiddenWords, ", ")))
	}
	sb.WriteString("Ensure all hooks, captions, and title variations rigorously match this brand voice.\n")

	return sb.String()
}

// AdaptImagePrompt converts a base subject into a prompt locked to the persona's visual style.
func AdaptImagePrompt(p *BrandPersona, baseSubject string) string {
	if p == nil || p.VisualStyle == "" {
		return fmt.Sprintf("Editorial 3D concept render illustrating '%s', modern cinematic lighting, 8k resolution", baseSubject)
	}

	cleanStyle := strings.ReplaceAll(p.VisualStyle, "_", " ")
	palette := p.ColorPalette
	if palette == "" {
		palette = "vibrant accents"
	}

	return fmt.Sprintf("An evocative, highly detailed visual in %s aesthetic illustrating '%s', color palette: %s, professional studio composition, 8k resolution, crisp detail",
		cleanStyle, baseSubject, palette)
}

// GeneratePersonaTitleVariations crafts all 3 CTR title variations aligned strictly with the brand persona's tone.
func GeneratePersonaTitleVariations(p *BrandPersona, subject, niche string) []string {
	cleanTone := ""
	if p != nil {
		cleanTone = strings.ToLower(strings.TrimSpace(p.Tone))
	}

	switch {
	case strings.Contains(cleanTone, "sarcastic") || strings.Contains(cleanTone, "witty"):
		return []string{
			fmt.Sprintf("Why %s is Not the Magic Solution Everyone Claims It Is", subject),
			fmt.Sprintf("The Uncomfortable Truth About %s Nobody in Tech Wants to Admit", subject),
			fmt.Sprintf("We Tested %s in Production So You Don't Have To (Honest Breakdown)", subject),
		}

	case strings.Contains(cleanTone, "provocative") || strings.Contains(cleanTone, "bold"):
		return []string{
			fmt.Sprintf("Why Ignoring %s Right Now Will Cost You Years in %s", subject, niche),
			fmt.Sprintf("The Brutal Reality of %s Most Creators Are Hiding", subject),
			fmt.Sprintf("How %s is Silently Destroying Legacy Workflows", subject),
		}

	case strings.Contains(cleanTone, "casual") || strings.Contains(cleanTone, "chill"):
		return []string{
			fmt.Sprintf("A Realistic Look at %s and Why People Actually Care", subject),
			fmt.Sprintf("What You Actually Need to Know About %s", subject),
			fmt.Sprintf("Trying Out %s: Simple Breakdown Without the Fluff", subject),
		}

	case strings.Contains(cleanTone, "academic") || strings.Contains(cleanTone, "technical"):
		return []string{
			fmt.Sprintf("An Empirical Analysis of %s: Architecture and Trade-Offs", subject),
			fmt.Sprintf("Benchmarking %s: Performance, Latency, and Scalability", subject),
			fmt.Sprintf("System Design Deep Dive: Deconstructing %s", subject),
		}

	default:
		return []string{
			fmt.Sprintf("Why %s is the Future of Tech (Complete Breakdown)", subject),
			fmt.Sprintf("The Breakthrough in %s Reshaping %s", subject, niche),
			fmt.Sprintf("Mastering %s: Everything You Need to Know", subject),
		}
	}
}

// FormatHookWithTone generates a hook in the specific requested brand tone.
func FormatHookWithTone(tone, subject, niche string) string {
	p := &BrandPersona{Tone: tone}
	vars := GeneratePersonaTitleVariations(p, subject, niche)
	if len(vars) > 0 {
		return vars[0]
	}
	return fmt.Sprintf("The real reason %s is trending across %s", subject, niche)
}

// Common buzzword mappings to natural, non-corporate English synonyms
var defaultBuzzwordSynonyms = map[string]string{
	"synergy":       "real integration",
	"paradigm":      "modern architecture",
	"delve":         "explore",
	"game-changer":  "major breakthrough",
	"game changer":  "major breakthrough",
	"gamechanger":   "major breakthrough",
	"plethora":      "wide range",
	"tapestry":      "ecosystem",
	"revolutionize": "improve",
	"unleash":       "unlock",
	"harness":       "use",
	"leverage":      "utilize",
	"cutting-edge":  "modern",
	"seamless":      "smooth",
}

// FilterForbiddenWords contextually rewrites banned buzzwords into natural, grammatically correct phrasing.
func FilterForbiddenWords(text string, forbidden []string) string {
	for _, word := range forbidden {
		cleanWord := strings.ToLower(strings.TrimSpace(word))
		if cleanWord == "" {
			continue
		}

		// Look for natural synonym substitution first
		replacement, hasSynonym := defaultBuzzwordSynonyms[cleanWord]
		if !hasSynonym {
			replacement = ""
		}

		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(cleanWord) + `\b`)
		text = re.ReplaceAllString(text, replacement)
	}

	// Clean up double spaces or dangling prepositions like "to into" -> "into"
	text = regexp.MustCompile(`\bto\s+into\b`).ReplaceAllString(text, "into")
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}
