package google_test

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/ai/google"
)

func testResolved(baseURL, modelID string, keys ...string) ai.ResolvedModel {
	if len(keys) == 0 {
		keys = []string{"test-key-1"}
	}
	return ai.ResolvedModel{
		Provider: ai.ProviderConfig{
			ID:      "google-test",
			Kind:    "google",
			BaseURL: baseURL,
			APIKeys: keys,
			Headers: map[string]string{"X-Custom-Header": "omni"},
		},
		Model: ai.ModelConfig{
			ID:         "m1",
			ProviderID: "google-test",
			Protocol:   google.ProtocolInteractions,
			ModelID:    modelID,
			Capabilities: ai.Capabilities{
				Vision:   true,
				AudioIn:  true,
				VideoIn:  true,
				ToolUse:  true,
				Thinking: true,
			},
		},
		Keys: ai.NewKeyRotator(keys),
	}
}

func TestInteractionsStatelessMultiStepAndNativeContent(t *testing.T) {
	var (
		callCount atomic.Int32
		seenKeys  []string
		turn2Body map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/interactions" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("X-Custom-Header"); got != "omni" {
			t.Errorf("X-Custom-Header = %q, want omni", got)
		}
		seenKeys = append(seenKeys, r.Header.Get("x-goog-api-key"))

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}

		// Verify store=false and step_list input on every request.
		if store, ok := body["store"].(bool); !ok || store {
			t.Errorf("store = %v, want false", body["store"])
		}

		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// Verify max_output_tokens guard (>= 2048) and thinking_level mapping on flash.
			gc, _ := body["generation_config"].(map[string]any)
			if gc["thinking_level"] != "high" {
				t.Errorf("flash xhigh thinking_level = %v, want high", gc["thinking_level"])
			}
			if gc["max_output_tokens"] != float64(2048) {
				t.Errorf("max_output_tokens = %v, want 2048", gc["max_output_tokens"])
			}
			_, _ = w.Write([]byte(`{
				"object": "interaction",
				"status": "requires_action",
				"steps": [
					{"type": "thought", "signature": "sig-abc-123"},
					{"type": "function_call", "id": "call_1", "name": "lookup_weather", "arguments": {"city": "Shanghai"}}
				],
				"usage": {
					"total_input_tokens": 120,
					"total_output_tokens": 0,
					"total_thought_tokens": 45,
					"total_tokens": 165
				}
			}`))
			return
		}

		turn2Body = body
		_, _ = w.Write([]byte(`{
			"object": "interaction",
			"status": "completed",
			"steps": [
				{"type": "model_output", "content": [{"type": "text", "text": "上海今天晴，24度"}]}
			],
			"usage": {
				"total_input_tokens": 210,
				"total_cached_tokens": 180,
				"total_output_tokens": 18,
				"total_thought_tokens": 0,
				"total_tokens": 228
			}
		}`))
	}))
	defer srv.Close()

	lm, err := google.NewLanguageModel(
		t.Context(),
		testResolved(srv.URL, "gemini-3.8-flash", "key-A", "key-B"),
	)
	if err != nil {
		t.Fatalf("NewLanguageModel: %v", err)
	}

	tools := []ai.ToolDef{{
		Name:        "lookup_weather",
		Description: "Fetch weather by city",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		},
	}}

	turn1Msgs := []ai.Message{{
		Role:  ai.RoleUser,
		Parts: []ai.Part{ai.TextPart("上海今天天气怎么样？")},
	}}

	resp1, err := lm.Generate(t.Context(), ai.GenerateRequest{
		System:          "你是群友",
		Messages:        turn1Msgs,
		Tools:           tools,
		ThinkingLevel:   "xhigh", // flash model must downgrade xhigh -> high
		MaxOutputTokens: 256,     // must clamp up to 2048
	})
	if err != nil {
		t.Fatalf("Generate turn 1: %v", err)
	}
	if resp1.Status != "requires_action" {
		t.Fatalf("resp1.Status = %q, want requires_action", resp1.Status)
	}
	if len(resp1.ToolCalls) != 1 || resp1.ToolCalls[0].ID != "call_1" || resp1.ToolCalls[0].Name != "lookup_weather" {
		t.Fatalf("unexpected tool calls: %+v", resp1.ToolCalls)
	}
	if resp1.Usage.OutputTokens == nil || *resp1.Usage.OutputTokens != 0 {
		t.Fatalf("expected explicit 0 OutputTokens, got %v", resp1.Usage.OutputTokens)
	}
	if resp1.Usage.CachedTokens != nil {
		t.Fatalf("expected nil CachedTokens when absent, got %v", *resp1.Usage.CachedTokens)
	}
	if len(resp1.NativeContent) == 0 {
		t.Fatal("expected non-empty NativeContent")
	}

	// Turn 2: carry NativeContent verbatim + function_result.
	turn2Msgs := []ai.Message{
		turn1Msgs[0],
		{
			Role:          ai.RoleAssistant,
			Parts:         resp1.Parts,
			NativeContent: resp1.NativeContent,
		},
		{
			Role: ai.RoleTool,
			Parts: []ai.Part{
				ai.ToolResultPart(ai.ToolResult{
					CallID:  "call_1",
					Name:    "lookup_weather",
					Content: `{"temp":24,"cond":"sunny"}`,
				}),
			},
		},
	}

	resp2, err := lm.Generate(t.Context(), ai.GenerateRequest{
		System:        "你是群友",
		Messages:      turn2Msgs,
		Tools:         tools,
		ThinkingLevel: "low",
	})
	if err != nil {
		t.Fatalf("Generate turn 2: %v", err)
	}
	if resp2.Text != "上海今天晴，24度" {
		t.Fatalf("resp2.Text = %q", resp2.Text)
	}
	if resp2.Usage.CachedTokens == nil || *resp2.Usage.CachedTokens != 180 {
		t.Fatalf("expected CachedTokens=180, got %v", resp2.Usage.CachedTokens)
	}

	// Verify key rotation across turn 1 and turn 2.
	if len(seenKeys) != 2 || seenKeys[0] != "key-A" || seenKeys[1] != "key-B" {
		t.Fatalf("seenKeys = %v, want [key-A key-B]", seenKeys)
	}

	// Verify turn 2 input steps preserved thought+signature, function_call, and function_result.
	rawInput, _ := turn2Body["input"].([]any)
	if len(rawInput) != 4 {
		t.Fatalf("turn2 input steps = %d, want 4 (%v)", len(rawInput), rawInput)
	}
	step1, _ := rawInput[1].(map[string]any)
	if step1["type"] != "thought" || step1["signature"] != "sig-abc-123" {
		t.Fatalf("expected preserved thought signature step, got %v", step1)
	}
	step2, _ := rawInput[2].(map[string]any)
	if step2["type"] != "function_call" || step2["id"] != "call_1" {
		t.Fatalf("expected preserved function_call step, got %v", step2)
	}
	step3, _ := rawInput[3].(map[string]any)
	if step3["type"] != "function_result" || step3["call_id"] != "call_1" {
		t.Fatalf("expected function_result step, got %v", step3)
	}
}

func TestFileAPIUploadPollingCacheAndExpiry(t *testing.T) {
	var (
		uploads      atomic.Int32
		gets         atomic.Int32
		lastVideoURI string
		lastRes      string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/upload/v1beta/files"):
			if r.Header.Get("X-Goog-Upload-Command") == "start" {
				w.Header().Set("X-Goog-Upload-URL", "http://"+r.Host+"/upload/v1beta/files/session1")
				w.Header().Set("X-Goog-Upload-Status", "active")
				w.WriteHeader(http.StatusOK)
				return
			}
			n := uploads.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("X-Goog-Upload-Status", "final")
			_, _ = w.Write([]byte(`{"file":{"name":"files/vid-` + string(rune('0'+n)) + `","uri":"http://` + r.Host + `/v1beta/files/vid-` + string(rune('0'+n)) + `","mimeType":"video/mp4","state":"PROCESSING"}}`))

		case strings.HasPrefix(r.URL.Path, "/v1beta/files/"):
			g := gets.Add(1)
			state := "PROCESSING"
			if g >= 2 {
				state = "ACTIVE"
			}
			name := strings.TrimPrefix(r.URL.Path, "/v1beta/")
			_, _ = w.Write([]byte(`{"name":"` + name + `","uri":"http://` + r.Host + r.URL.Path + `","mimeType":"video/mp4","state":"` + state + `"}`))

		case r.URL.Path == "/v1beta/interactions":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			steps := body["input"].([]any)
			uStep := steps[0].(map[string]any)
			content := uStep["content"].([]any)
			vid := content[0].(map[string]any)
			lastVideoURI, _ = vid["uri"].(string)
			lastRes, _ = vid["resolution"].(string)

			_, _ = w.Write([]byte(`{
				"object": "interaction",
				"status": "completed",
				"steps": [{"type": "model_output", "content": [{"type": "text", "text": "视频里是一只猫"}]}]
			}`))

		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	lm, err := google.NewLanguageModel(
		t.Context(),
		testResolved(srv.URL, "gemini-4-hydrogen"),
		google.WithClock(clock),
		google.WithFileCacheTTL(44*time.Hour),
		google.WithFilePollConfig(5*time.Millisecond, 5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewLanguageModel: %v", err)
	}

	req := ai.GenerateRequest{
		Messages: []ai.Message{{
			Role: ai.RoleUser,
			Parts: []ai.Part{
				ai.MediaPart(ai.PartVideo, ai.MediaRef{
					BlobID:   "blob-video-1",
					MIMEType: "video/mp4",
					Data:     []byte("fake-mp4-bytes"),
				}),
			},
		}},
	}

	// Call 1: uploads video + polls PROCESSING -> ACTIVE.
	resp1, err := lm.Generate(t.Context(), req)
	if err != nil {
		t.Fatalf("Generate #1: %v", err)
	}
	if resp1.Text != "视频里是一只猫" {
		t.Fatalf("resp1.Text = %q", resp1.Text)
	}
	if !strings.Contains(lastVideoURI, "/v1beta/files/vid-1") {
		t.Fatalf("expected File API URI, got %q", lastVideoURI)
	}
	if lastRes != "low" {
		t.Fatalf("expected default video resolution=low, got %q", lastRes)
	}
	if uploads.Load() != 1 {
		t.Fatalf("uploads = %d, want 1", uploads.Load())
	}
	if gets.Load() < 2 {
		t.Fatalf("expected polling GETs >= 2, got %d", gets.Load())
	}
	// Verify zero files/... URI leaked into ai.GenerateResponse.
	if strings.Contains(resp1.Text, "files/") || strings.Contains(string(resp1.NativeContent), "files/") {
		t.Fatalf("files URI leaked into response: %+v", resp1)
	}

	// Call 2 (within 44h TTL): cache hit, zero extra uploads!
	now = now.Add(43 * time.Hour)
	if _, err := lm.Generate(t.Context(), req); err != nil {
		t.Fatalf("Generate #2: %v", err)
	}
	if uploads.Load() != 1 {
		t.Fatalf("expected cache hit (uploads=1), got %d", uploads.Load())
	}

	// Call 3 (past 44h TTL): cache expired, re-uploads!
	now = now.Add(2 * time.Hour)
	if _, err := lm.Generate(t.Context(), req); err != nil {
		t.Fatalf("Generate #3: %v", err)
	}
	if uploads.Load() != 2 {
		t.Fatalf("expected re-upload after 44h expiry (uploads=2), got %d", uploads.Load())
	}
}

func TestDecisionsAPIAllQuestionTypes(t *testing.T) {
	var seenBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/decisions" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seenBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"answers": {
				"should_reply": {"type": "noul", "noul": 0.87, "confidence": 0.87},
				"delta_P": {"type": "score", "score": 3.5, "confidence": 0.6, "probabilities": {"0":0.0,"1":0.0,"2":0.1,"3":0.3,"4":0.6}, "legend": {"0":"bad","4":"great"}},
				"tone": {"type": "choice", "choice": "warm", "confidence": 0.91, "probabilities": {"warm":0.91,"cold":0.09}}
			},
			"usage": {
				"total_input_tokens": 467,
				"total_output_tokens": 0,
				"total_tokens": 467
			}
		}`))
	}))
	defer srv.Close()

	dm, err := google.NewDecisionModel(t.Context(), testResolved(srv.URL, "models/gemini-decision-1"))
	if err != nil {
		t.Fatalf("NewDecisionModel: %v", err)
	}

	res, err := dm.Decide(t.Context(), ai.DecisionRequest{
		Input: "群友: 刚拿到 offer 了！请大家喝奶茶！",
		Questions: map[string]ai.DecisionQuestion{
			"should_reply": {
				Kind:             ai.DecisionNoul,
				Instructions:     "是否适合接话",
				TrueDescription:  "适合接话",
				FalseDescription: "安静看着",
			},
			"delta_P": {
				Kind:         ai.DecisionScore,
				Instructions: "愉悦度刺激",
				Criteria:     []string{"很糟", "略糟", "中性", "开心", "很开心"},
			},
			"tone": {
				Kind:         ai.DecisionChoice,
				Instructions: "氛围倾向",
				Choices:      map[string]string{"warm": "热烈友好", "cold": "冷淡疏离"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}

	// Verify criteria for score was sent as JSON array ([]string), not map/legend.
	qs := seenBody["questions"].(map[string]any)
	dpQ := qs["delta_P"].(map[string]any)
	if _, isSlice := dpQ["criteria"].([]any); !isSlice {
		t.Fatalf("score question criteria must be JSON array, got %T", dpQ["criteria"])
	}

	sr := res.Answers["should_reply"]
	if sr.Kind != ai.DecisionNoul || math.Abs(sr.Probability-0.87) > 1e-9 {
		t.Fatalf("unexpected should_reply: %+v", sr)
	}

	dp := res.Answers["delta_P"]
	if dp.Kind != ai.DecisionScore || math.Abs(dp.Score-3.5) > 1e-9 || math.Abs(dp.NormalizedScore-0.75) > 1e-9 {
		t.Fatalf("unexpected delta_P: %+v", dp)
	}

	tone := res.Answers["tone"]
	if tone.Kind != ai.DecisionChoice || tone.Choice != "warm" || math.Abs(tone.Confidence-0.91) > 1e-9 {
		t.Fatalf("unexpected tone: %+v", tone)
	}

	if res.Usage.InputTokens == nil || *res.Usage.InputTokens != 467 {
		t.Fatalf("InputTokens = %v, want 467", res.Usage.InputTokens)
	}
	if res.Usage.OutputTokens == nil || *res.Usage.OutputTokens != 0 {
		t.Fatalf("OutputTokens = %v, want *0", res.Usage.OutputTokens)
	}
}

func TestGoogleErrorClassificationAndNoSDKRetries(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantKind  ai.ErrorKind
		retryable bool
	}{
		{"401 auth", http.StatusUnauthorized, `{"error":{"code":"unauthenticated","message":"bad api key"}}`, ai.ErrAuth, false},
		{"400 invalid arg", http.StatusBadRequest, `{"error":{"code":"invalid_argument","message":"bad param"}}`, ai.ErrInvalidArg, false},
		{"400 context too long", http.StatusBadRequest, `{"error":{"code":"invalid_argument","message":"input token count exceeds the maximum number of tokens"}}`, ai.ErrContextTooLong, false},
		{"429 rate limit", http.StatusTooManyRequests, `{"error":{"code":"resource_exhausted","message":"rate limited"}}`, ai.ErrRateLimit, true},
		{"503 transient", http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"overloaded"}}`, ai.ErrTransient, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			lm, err := google.NewLanguageModel(t.Context(), testResolved(srv.URL, "gemini-4-hydrogen"))
			if err != nil {
				t.Fatalf("NewLanguageModel: %v", err)
			}
			_, err = lm.Generate(t.Context(), ai.GenerateRequest{
				Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{ai.TextPart("hi")}}},
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if hits.Load() != 1 {
				t.Fatalf("SDK auto-retried! hits = %d, want 1", hits.Load())
			}
			if got := ai.ErrorKindOf(err); got != tc.wantKind {
				t.Fatalf("ErrorKindOf(err) = %q, want %q (err=%v)", got, tc.wantKind, err)
			}
			if got := ai.IsRetryable(err); got != tc.retryable {
				t.Fatalf("IsRetryable(err) = %v, want %v", got, tc.retryable)
			}
		})
	}
}

func TestNormalizeThinkingLevel(t *testing.T) {
	cases := []struct {
		model   string
		in      string
		want    string
		wantErr bool
	}{
		{"gemini-3.8-flash", "", "", false},
		{"gemini-3.8-flash", "minimal", "low", false},
		{"gemini-3.8-flash", "xhigh", "high", false},
		{"gemini-4-hydrogen", "xhigh", "xhigh", false},
		{"gemini-4-hydrogen", "minimal", "low", false},
		{"gemini-4-hydrogen", "turbo", "", true},
	}
	for _, tc := range cases {
		got, err := google.NormalizeThinkingLevel("g", tc.model, tc.in)
		if tc.wantErr {
			if err == nil || ai.ErrorKindOf(err) != ai.ErrInvalidArg {
				t.Fatalf("(%s, %s) err = %v, want invalid_arg", tc.model, tc.in, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("(%s, %s) unexpected err: %v", tc.model, tc.in, err)
		}
		var gotStr string
		if got != nil {
			gotStr = string(*got)
		}
		if gotStr != tc.want {
			t.Fatalf("(%s, %s) = %q, want %q", tc.model, tc.in, gotStr, tc.want)
		}
	}
}

func TestContextCanceledNotRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	lm, err := google.NewLanguageModel(t.Context(), testResolved(srv.URL, "gemini-4-hydrogen"))
	if err != nil {
		t.Fatalf("NewLanguageModel: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = lm.Generate(ctx, ai.GenerateRequest{
		Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{ai.TextPart("hi")}}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if ai.IsRetryable(err) {
		t.Fatalf("canceled request must not be retryable: %v", err)
	}
}
