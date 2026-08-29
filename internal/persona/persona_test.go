package persona

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

func TestPersona_SetAndGetBrandPersona(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed initializing sqlmock: %v", err)
	}
	defer db.Close()

	testUserID := uuid.New().String()
	personaID := uuid.New().String()
	now := time.Now().UTC()

	svc := NewService(db)

	// Mock SetBrandPersona
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO brand_personas")).
		WithArgs(sqlmock.AnyArg(), testUserID, "CyberTech", "sarcastic", "cyberpunk_dark_neon", "#00FFCC, #FF007F",
			"Be witty and concise.", models.StringArray{"synergy", "delve"}, "Tech devs", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(personaID, now, now))

	p, err := svc.SetBrandPersona(context.Background(), &SetPersonaRequest{
		UserID:          testUserID,
		BrandName:       "CyberTech",
		Tone:            "sarcastic",
		VisualStyle:     "cyberpunk_dark_neon",
		ColorPalette:    "#00FFCC, #FF007F",
		VoiceGuidelines: "Be witty and concise.",
		ForbiddenWords:  []string{"synergy", "delve"},
		TargetAudience:  "Tech devs",
	})
	if err != nil {
		t.Fatalf("SetBrandPersona failed: %v", err)
	}

	if p.Tone != "sarcastic" || p.VisualStyle != "cyberpunk_dark_neon" {
		t.Errorf("unexpected persona tone or style: %+v", p)
	}

	// Mock GetBrandPersona
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, user_id, brand_name, tone, visual_style, color_palette, voice_guidelines, forbidden_words, target_audience, is_default, created_at, updated_at FROM brand_personas")).
		WithArgs(testUserID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "brand_name", "tone", "visual_style", "color_palette",
			"voice_guidelines", "forbidden_words", "target_audience", "is_default", "created_at", "updated_at",
		}).AddRow(personaID, testUserID, "CyberTech", "sarcastic", "cyberpunk_dark_neon", "#00FFCC, #FF007F",
			"Be witty and concise.", models.StringArray{"synergy", "delve"}, "Tech devs", true, now, now))

	fetched, err := svc.GetBrandPersona(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("GetBrandPersona failed: %v", err)
	}
	if fetched.BrandName != "CyberTech" || fetched.Tone != "sarcastic" {
		t.Errorf("fetched persona mismatch: %+v", fetched)
	}
}

func TestPersona_ToneAndPromptAdaptation(t *testing.T) {
	p := &BrandPersona{
		BrandName:       "DevWits",
		Tone:            "sarcastic",
		VisualStyle:     "cyberpunk_dark_neon",
		ColorPalette:    "Neon Cyan & Magenta",
		VoiceGuidelines: "Never use corporate jargon; keep sentences under 15 words.",
		ForbiddenWords:  []string{"synergy", "paradigm", "delve"},
		TargetAudience:  "Engineers",
	}

	prompt := BuildPromptInstructions(p)
	if !strings.Contains(prompt, "SARCASTIC") {
		t.Errorf("expected SARCASTIC in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "synergy, paradigm, delve") {
		t.Errorf("expected forbidden words in prompt, got: %s", prompt)
	}

	// Test Visual Style Adaptation
	imgPrompt := AdaptImagePrompt(p, "Open Source AI Revolution")
	if !strings.Contains(imgPrompt, "cyberpunk dark neon") || !strings.Contains(imgPrompt, "Neon Cyan & Magenta") {
		t.Errorf("image prompt did not adapt to brand visual aesthetic: %s", imgPrompt)
	}

	// Test Hook Tone Adaptation
	sarcasticHook := FormatHookWithTone(p.Tone, "LLM Tooling", "ai_tech")
	if !strings.Contains(sarcasticHook, "Magic Solution") && !strings.Contains(sarcasticHook, "under the hood") {
		t.Errorf("expected sarcastic phrasing in hook: %s", sarcasticHook)
	}

	// Test All 3 Persona Title Variations
	vars := GeneratePersonaTitleVariations(p, "Modern AI Frameworks", "ai_tech")
	if len(vars) != 3 {
		t.Fatalf("expected 3 title variations, got %d", len(vars))
	}
	if !strings.Contains(vars[0], "Magic Solution") {
		t.Errorf("variation 1 did not adopt sarcastic tone: %s", vars[0])
	}
	if !strings.Contains(vars[1], "Uncomfortable Truth") {
		t.Errorf("variation 2 did not adopt sarcastic tone: %s", vars[1])
	}
	if !strings.Contains(vars[2], "We Tested") {
		t.Errorf("variation 3 did not adopt sarcastic tone: %s", vars[2])
	}

	// Test Forbidden Words Contextual Rewrite (Preserving Complete Grammar)
	cleanText := FilterForbiddenWords("This game-changer model creates synergy and allows developers to delve into new paradigm workflows.", p.ForbiddenWords)
	if strings.Contains(strings.ToLower(cleanText), "delve") || strings.Contains(strings.ToLower(cleanText), "paradigm") || strings.Contains(strings.ToLower(cleanText), "synergy") {
		t.Errorf("forbidden words were not filtered: %s", cleanText)
	}
	// Verify grammatical completeness (contains natural replacement words like "explore", "modern architecture")
	if !strings.Contains(cleanText, "explore") || !strings.Contains(cleanText, "real integration") {
		t.Errorf("sentence lost grammatical flow or synonyms: %s", cleanText)
	}
}
