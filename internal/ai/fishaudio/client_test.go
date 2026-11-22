package fishaudio_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"omni-bot/internal/ai"
	"omni-bot/internal/ai/fishaudio"
)

func TestSynthesizePassesModelHeaderAndFormatsBody(t *testing.T) {
	var (
		seenModelHeader string
		seenAuths       []string
		seenBodies      []map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tts" {
			http.Error(w, "bad path "+r.URL.Path, http.StatusNotFound)
			return
		}
		seenModelHeader = r.Header.Get("model")
		seenAuths = append(seenAuths, r.Header.Get("Authorization"))

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		seenBodies = append(seenBodies, body)

		if body["format"] == "opus" {
			w.Header().Set("Content-Type", "audio/opus")
			_, _ = w.Write([]byte("OggS-fake-opus"))
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg; charset=binary")
		_, _ = w.Write([]byte{0xff, 0xfb, 0x90, 0xc4})
	}))
	defer srv.Close()

	client, err := fishaudio.New(ai.ResolvedModel{
		Provider: ai.ProviderConfig{
			ID:      "fish",
			Kind:    "fishaudio",
			BaseURL: srv.URL + "/v1",
			APIKeys: []string{"fk-1", "fk-2"},
		},
		Model: ai.ModelConfig{
			ID:         "tts-main",
			ProviderID: "fish",
			Protocol:   fishaudio.Protocol,
			ModelID:    "s2.1-pro-free",
		},
		Keys: ai.NewKeyRotator([]string{"fk-1", "fk-2"}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Call 1: default mp3, no reference_id.
	resp1, err := client.Synthesize(t.Context(), ai.SpeechRequest{Text: "你好呀"})
	if err != nil {
		t.Fatalf("Synthesize #1: %v", err)
	}
	if seenModelHeader != "s2.1-pro-free" {
		t.Fatalf("model header = %q, want s2.1-pro-free", seenModelHeader)
	}
	if _, hasModelInBody := seenBodies[0]["model"]; hasModelInBody {
		t.Fatalf("model must NOT be sent in JSON body: %v", seenBodies[0])
	}
	if _, hasRef := seenBodies[0]["reference_id"]; hasRef {
		t.Fatalf("empty reference_id should be omitted: %v", seenBodies[0])
	}
	if seenBodies[0]["format"] != "mp3" {
		t.Fatalf("format = %v, want mp3", seenBodies[0]["format"])
	}
	if resp1.MIMEType != "audio/mpeg" {
		t.Fatalf("MIMEType = %q, want audio/mpeg", resp1.MIMEType)
	}
	if !bytes.Equal(resp1.Audio, []byte{0xff, 0xfb, 0x90, 0xc4}) {
		t.Fatalf("unexpected mp3 bytes: %x", resp1.Audio)
	}

	// Call 2: explicit opus + reference_id + model override.
	resp2, err := client.Synthesize(t.Context(), ai.SpeechRequest{
		Text:    "晚上好",
		Format:  "opus",
		VoiceID: "voice-101",
		Model:   "s2.1-pro",
	})
	if err != nil {
		t.Fatalf("Synthesize #2: %v", err)
	}
	if seenModelHeader != "s2.1-pro" {
		t.Fatalf("model header override = %q, want s2.1-pro", seenModelHeader)
	}
	if seenBodies[1]["reference_id"] != "voice-101" {
		t.Fatalf("reference_id = %v, want voice-101", seenBodies[1]["reference_id"])
	}
	if resp2.MIMEType != "audio/opus" || string(resp2.Audio) != "OggS-fake-opus" {
		t.Fatalf("unexpected opus response: %+v", resp2)
	}
	if len(seenAuths) != 2 || seenAuths[0] != "Bearer fk-1" || seenAuths[1] != "Bearer fk-2" {
		t.Fatalf("unexpected auth rotation: %v", seenAuths)
	}
}

func TestSynthesizeErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		wantKind  ai.ErrorKind
		retryable bool
	}{
		{"401", http.StatusUnauthorized, ai.ErrAuth, false},
		{"429", http.StatusTooManyRequests, ai.ErrRateLimit, true},
		{"503", http.StatusServiceUnavailable, ai.ErrTransient, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("error body"))
			}))
			defer srv.Close()

			client, err := fishaudio.New(ai.ResolvedModel{
				Provider: ai.ProviderConfig{ID: "fish", BaseURL: srv.URL, APIKeys: []string{"k"}},
				Model:    ai.ModelConfig{ID: "t", ProviderID: "fish", Protocol: fishaudio.Protocol, ModelID: "s2.1-pro-free"},
				Keys:     ai.NewKeyRotator([]string{"k"}),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			_, err = client.Synthesize(t.Context(), ai.SpeechRequest{Text: "hi"})
			if err == nil {
				t.Fatal("expected error")
			}
			if hits.Load() != 1 {
				t.Fatalf("hits = %d, want 1", hits.Load())
			}
			if got := ai.ErrorKindOf(err); got != tc.wantKind {
				t.Fatalf("ErrorKindOf = %q, want %q", got, tc.wantKind)
			}
			if got := ai.IsRetryable(err); got != tc.retryable {
				t.Fatalf("IsRetryable = %v, want %v", got, tc.retryable)
			}
		})
	}
}
