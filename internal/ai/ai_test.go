package ai_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"omni-bot/internal/ai"
)

func TestKeyRotator(t *testing.T) {
	t.Run("empty returns auth error", func(t *testing.T) {
		r := ai.NewKeyRotator([]string{"", "   "})
		if r.Len() != 0 {
			t.Fatalf("Len() = %d, want 0", r.Len())
		}
		_, err := r.Next()
		if err == nil {
			t.Fatal("expected error for empty rotator")
		}
		if got := ai.ErrorKindOf(err); got != ai.ErrAuth {
			t.Fatalf("ErrorKindOf(err) = %q, want %q", got, ai.ErrAuth)
		}
		if ai.IsRetryable(err) {
			t.Fatal("auth error must not be retryable")
		}
	})

	t.Run("round robin ordering", func(t *testing.T) {
		r := ai.NewKeyRotator([]string{" k1 ", "", "k2", "k3"})
		if r.Len() != 3 {
			t.Fatalf("Len() = %d, want 3", r.Len())
		}
		want := []string{"k1", "k2", "k3", "k1", "k2", "k3"}
		for i, w := range want {
			got, err := r.Next()
			if err != nil {
				t.Fatalf("call %d unexpected err: %v", i, err)
			}
			if got != w {
				t.Fatalf("call %d = %q, want %q", i, got, w)
			}
		}
	})

	t.Run("concurrent distribution", func(t *testing.T) {
		r := ai.NewKeyRotator([]string{"a", "b", "c"})
		const workers = 6
		const perWorker = 50

		var (
			mu     sync.Mutex
			counts = map[string]int{}
			wg     sync.WaitGroup
		)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				local := map[string]int{}
				for range perWorker {
					k, err := r.Next()
					if err != nil {
						t.Errorf("Next() err = %v", err)
						return
					}
					local[k]++
				}
				mu.Lock()
				for k, v := range local {
					counts[k] += v
				}
				mu.Unlock()
			}()
		}
		wg.Wait()

		for _, k := range []string{"a", "b", "c"} {
			if counts[k] != 100 {
				t.Fatalf("key %q count = %d, want 100", k, counts[k])
			}
		}
	})
}

func TestErrorClassificationAndRetryability(t *testing.T) {
	statusCases := []struct {
		name      string
		status    int
		body      string
		wantKind  ai.ErrorKind
		retryable bool
	}{
		{"401 auth", http.StatusUnauthorized, `{"error":"bad key"}`, ai.ErrAuth, false},
		{"403 auth", http.StatusForbidden, "forbidden", ai.ErrAuth, false},
		{"400 invalid arg", http.StatusBadRequest, "invalid thinking_level", ai.ErrInvalidArg, false},
		{"400 context too long", http.StatusBadRequest, "input token count exceeds the maximum number of tokens", ai.ErrContextTooLong, false},
		{"413 payload too large", http.StatusRequestEntityTooLarge, "too large", ai.ErrContextTooLong, false},
		{"429 rate limit", http.StatusTooManyRequests, "quota exceeded", ai.ErrRateLimit, true},
		{"500 transient", http.StatusInternalServerError, "internal error", ai.ErrTransient, true},
		{"503 transient", http.StatusServiceUnavailable, "overloaded", ai.ErrTransient, true},
	}

	for _, tc := range statusCases {
		t.Run(tc.name, func(t *testing.T) {
			kind := ai.ClassifyHTTPStatus(tc.status, tc.body)
			if kind != tc.wantKind {
				t.Fatalf("ClassifyHTTPStatus(%d, %q) = %q, want %q", tc.status, tc.body, kind, tc.wantKind)
			}
			err := fmt.Errorf("wrapped: %w", &ai.Error{
				Kind:       kind,
				StatusCode: tc.status,
				Provider:   "test",
				Message:    tc.body,
			})
			if got := ai.ErrorKindOf(err); got != tc.wantKind {
				t.Fatalf("ErrorKindOf(err) = %q, want %q", got, tc.wantKind)
			}
			if got := ai.IsRetryable(err); got != tc.retryable {
				t.Fatalf("IsRetryable(err) = %v, want %v", got, tc.retryable)
			}
		})
	}

	t.Run("context errors", func(t *testing.T) {
		if ai.IsRetryable(context.Canceled) {
			t.Fatal("context.Canceled must not be retryable")
		}
		wrappedCancel := &ai.Error{Kind: ai.ErrTransient, Err: context.Canceled}
		if ai.IsRetryable(wrappedCancel) {
			t.Fatal("wrapped context.Canceled must not be retryable")
		}
		if !ai.IsRetryable(context.DeadlineExceeded) {
			t.Fatal("context.DeadlineExceeded should be retryable as transient")
		}
		if ai.ErrorKindOf(errors.New("plain")) != ai.ErrUnknown {
			t.Fatal("plain error should classify as unknown")
		}
	})
}

type stubLanguageModel struct{ caps ai.Capabilities }

func (s stubLanguageModel) Generate(_ context.Context, _ ai.GenerateRequest) (ai.GenerateResponse, error) {
	return ai.GenerateResponse{Text: "ok"}, nil
}
func (s stubLanguageModel) Capabilities() ai.Capabilities { return s.caps }

type stubDecisionModel struct{}

func (stubDecisionModel) Decide(_ context.Context, _ ai.DecisionRequest) (ai.DecisionResult, error) {
	return ai.DecisionResult{}, nil
}

type stubEmbeddingModel struct{}

func (stubEmbeddingModel) Embed(_ context.Context, _ ai.EmbedRequest) (ai.EmbedResponse, error) {
	return ai.EmbedResponse{}, nil
}

type stubSpeechModel struct{}

func (stubSpeechModel) Synthesize(_ context.Context, _ ai.SpeechRequest) (ai.SpeechResponse, error) {
	return ai.SpeechResponse{}, nil
}

type stubTranscriptionModel struct{}

func (stubTranscriptionModel) Transcribe(_ context.Context, _ ai.TranscribeRequest) (ai.TranscribeResponse, error) {
	return ai.TranscribeResponse{}, nil
}

func TestRegistrySlotResolution(t *testing.T) {
	reg := ai.NewRegistry()

	if err := reg.RegisterProvider(ai.ProviderConfig{
		ID:      "google-main",
		Kind:    "google",
		BaseURL: "https://example.test",
		APIKeys: []string{"gk-1", "gk-2"},
		Headers: map[string]string{"X-Test": "1"},
	}); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := reg.RegisterProvider(ai.ProviderConfig{
		ID:      "local-whisper",
		Kind:    "whisper",
		BaseURL: "http://127.0.0.1:8178",
	}); err != nil {
		t.Fatalf("RegisterProvider whisper: %v", err)
	}

	models := []ai.ModelConfig{
		{ID: "chat-m", ProviderID: "google-main", Protocol: "gemini-interactions", ModelID: "gemini-4-hydrogen", Capabilities: ai.Capabilities{Vision: true, AudioIn: true, VideoIn: true, ToolUse: true, Thinking: true}},
		{ID: "summary-m", ProviderID: "google-main", Protocol: "gemini-interactions", ModelID: "gemini-3.8-flash", Capabilities: ai.Capabilities{Vision: true}},
		{ID: "vision-m", ProviderID: "google-main", Protocol: "gemini-interactions", ModelID: "gemini-3.8-flash", Capabilities: ai.Capabilities{Vision: true}},
		{ID: "decision-m", ProviderID: "google-main", Protocol: "gemini-decisions", ModelID: "models/gemini-decision-1"},
		{ID: "embed-m", ProviderID: "google-main", Protocol: "openai-embedding", ModelID: "Qwen/Qwen3-Embedding-4B"},
		{ID: "tts-m", ProviderID: "google-main", Protocol: "fishaudio-tts", ModelID: "s2.1-pro-free"},
		{ID: "asr-m", ProviderID: "local-whisper", Protocol: "whisper-stt", ModelID: "ggml-base.bin"},
	}
	for _, m := range models {
		if err := reg.RegisterModel(m); err != nil {
			t.Fatalf("RegisterModel(%s): %v", m.ID, err)
		}
	}

	slotBindings := map[ai.Slot]string{
		ai.SlotChat:      "chat-m",
		ai.SlotSummary:   "summary-m",
		ai.SlotVision:    "vision-m",
		ai.SlotDecision:  "decision-m",
		ai.SlotEmbedding: "embed-m",
		ai.SlotTTS:       "tts-m",
		ai.SlotASR:       "asr-m",
	}
	for slot, id := range slotBindings {
		if err := reg.BindSlot(slot, id); err != nil {
			t.Fatalf("BindSlot(%s): %v", slot, err)
		}
	}

	var langFactoryCalls int
	reg.RegisterLanguageFactory("gemini-interactions", func(rm ai.ResolvedModel) (ai.LanguageModel, error) {
		langFactoryCalls++
		return stubLanguageModel{caps: rm.Model.Capabilities}, nil
	})
	reg.RegisterDecisionFactory("gemini-decisions", func(ai.ResolvedModel) (ai.DecisionModel, error) {
		return stubDecisionModel{}, nil
	})
	reg.RegisterEmbeddingFactory("openai-embedding", func(ai.ResolvedModel) (ai.EmbeddingModel, error) {
		return stubEmbeddingModel{}, nil
	})
	reg.RegisterSpeechFactory("fishaudio-tts", func(ai.ResolvedModel) (ai.SpeechModel, error) {
		return stubSpeechModel{}, nil
	})
	reg.RegisterTranscriptionFactory("whisper-stt", func(ai.ResolvedModel) (ai.TranscriptionModel, error) {
		return stubTranscriptionModel{}, nil
	})

	lm1, err := reg.Language(ai.SlotChat)
	if err != nil {
		t.Fatalf("Language(chat): %v", err)
	}
	lm2, err := reg.Language(ai.SlotChat)
	if err != nil {
		t.Fatalf("Language(chat) second call: %v", err)
	}
	if !lm1.Capabilities().VideoIn || !lm2.Capabilities().Thinking {
		t.Fatalf("unexpected capabilities: %+v", lm1.Capabilities())
	}
	if langFactoryCalls != 1 {
		t.Fatalf("expected cached instance with 1 factory call, got %d", langFactoryCalls)
	}

	if _, err := reg.Language(ai.SlotSummary); err != nil {
		t.Fatalf("Language(summary): %v", err)
	}
	if _, err := reg.Language(ai.SlotVision); err != nil {
		t.Fatalf("Language(vision): %v", err)
	}
	if _, err := reg.Decision(ai.SlotDecision); err != nil {
		t.Fatalf("Decision(decision): %v", err)
	}
	if _, err := reg.Embedding(ai.SlotEmbedding); err != nil {
		t.Fatalf("Embedding(embedding): %v", err)
	}
	if _, err := reg.Speech(ai.SlotTTS); err != nil {
		t.Fatalf("Speech(tts): %v", err)
	}
	if _, err := reg.Transcription(ai.SlotASR); err != nil {
		t.Fatalf("Transcription(asr): %v", err)
	}

	// Verify provider key rotation via Registry.
	k1, err := reg.NextAPIKey("google-main")
	if err != nil || k1 != "gk-1" {
		t.Fatalf("NextAPIKey #1 = (%q, %v), want (gk-1, nil)", k1, err)
	}
	k2, err := reg.NextAPIKey("google-main")
	if err != nil || k2 != "gk-2" {
		t.Fatalf("NextAPIKey #2 = (%q, %v), want (gk-2, nil)", k2, err)
	}

	// Missing slot/model/provider errors.
	if _, err := reg.ResolveSlot("nonexistent"); ai.ErrorKindOf(err) != ai.ErrInvalidArg {
		t.Fatalf("missing slot kind = %q, want invalid_arg", ai.ErrorKindOf(err))
	}
	if err := reg.BindSlot("broken", "missing-model"); err != nil {
		t.Fatalf("BindSlot broken: %v", err)
	}
	if _, err := reg.ResolveSlot("broken"); ai.ErrorKindOf(err) != ai.ErrInvalidArg {
		t.Fatalf("missing model kind = %q, want invalid_arg", ai.ErrorKindOf(err))
	}
}
