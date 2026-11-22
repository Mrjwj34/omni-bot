//go:build integration

package google_test

import (
	"os"
	"testing"

	"omni-bot/internal/ai"
	"omni-bot/internal/ai/google"
	"omni-bot/internal/envload"
)

func TestLiveGeminiInteractionsAndDecisions(t *testing.T) {
	_, _ = envload.LoadRepoEnv(".")
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}
	baseURL := os.Getenv("GEMINI_API_BASE")
	modelID := os.Getenv("CHAT_MODEL")
	if modelID == "" {
		modelID = "gemini-3.8-flash"
	}

	lm, err := google.NewLanguageModel(t.Context(), ai.ResolvedModel{
		Provider: ai.ProviderConfig{ID: "google-live", Kind: "google", BaseURL: baseURL, APIKeys: []string{apiKey}},
		Model:    ai.ModelConfig{ID: "live-chat", ProviderID: "google-live", Protocol: google.ProtocolInteractions, ModelID: modelID},
		Keys:     ai.NewKeyRotator([]string{apiKey}),
	})
	if err != nil {
		t.Fatalf("NewLanguageModel: %v", err)
	}
	resp, err := lm.Generate(t.Context(), ai.GenerateRequest{
		Messages:      []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{ai.TextPart("Reply with exactly: OK")}}},
		ThinkingLevel: "low",
	})
	if err != nil {
		t.Fatalf("Generate live: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("empty live response")
	}

	decKey := os.Getenv("DECISION_API_KEY")
	if decKey == "" {
		decKey = apiKey
	}
	decBase := os.Getenv("DECISION_API_BASE")
	if decBase == "" {
		decBase = baseURL
	}
	decModel := os.Getenv("DECISION_MODEL")
	if decModel == "" {
		decModel = "models/gemini-decision-1"
	}
	dm, err := google.NewDecisionModel(t.Context(), ai.ResolvedModel{
		Provider: ai.ProviderConfig{ID: "google-dec", Kind: "google", BaseURL: decBase, APIKeys: []string{decKey}},
		Model:    ai.ModelConfig{ID: "live-dec", ProviderID: "google-dec", Protocol: google.ProtocolDecisions, ModelID: decModel},
		Keys:     ai.NewKeyRotator([]string{decKey}),
	})
	if err != nil {
		t.Fatalf("NewDecisionModel: %v", err)
	}
	decRes, err := dm.Decide(t.Context(), ai.DecisionRequest{
		Input: "群友: 大家晚上好！",
		Questions: map[string]ai.DecisionQuestion{
			"greet": {Kind: ai.DecisionNoul, Instructions: "Is this a friendly greeting?"},
		},
	})
	if err != nil {
		t.Fatalf("Decide live: %v", err)
	}
	if _, ok := decRes.Answers["greet"]; !ok {
		t.Fatal("missing greet answer")
	}
}
