package kernel

import (
	"bytes"
	"encoding/json"
	"image/color"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/memory"
	"omni-bot/internal/persona"
	"omni-bot/internal/store"
)

func TestHTTPAPI_FullContractAndSecretRedaction(t *testing.T) {
	ctx := t.Context()
	dataDir := t.TempDir()
	sandboxDir := t.TempDir()

	chatModel := &stubLanguageModel{
		fn: func(call int, req ai.GenerateRequest) (ai.GenerateResponse, error) {
			// Reflection requests carry a JSON ResponseSchema.
			if req.ResponseSchema != nil {
				return ai.GenerateResponse{
					Text: `{"no_change":false,"rationale":"新增猫猫话题倾向","operations":[{"op":"add","content":"我看到群友晒猫照片时总会忍不住多夸两句小猫咪。"}]}`,
				}, nil
			}
			return ai.GenerateResponse{
				Text:  "收到调试消息啦！",
				Usage: ai.Usage{InputTokens: ai.IntPtr(80), OutputTokens: ai.IntPtr(12), CachedTokens: ai.IntPtr(40)},
			}, nil
		},
	}
	embedder := &stubEmbeddingModel{dim: memory.DefaultVectorDim}

	initAI := ai.NewRegistry()
	_ = initAI.BindSlot(ai.SlotChat, "chat-test")
	_ = initAI.BindSlot(ai.SlotEmbedding, "emb-test")
	initAI.SetLanguageModel("chat-test", chatModel)
	initAI.SetEmbeddingModel("emb-test", embedder)

	rt, err := NewRuntime(ctx, RuntimeOptions{
		DataDir:        dataDir,
		SandboxDir:     sandboxDir,
		InitialAI:      initAI,
		DisableAsyncBg: true,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	defer func() { _ = rt.Close(t.Context()) }()

	// Seed a memory + PAD history entry so dashboard, memory, and PAD endpoints have real rows.
	nowMs := time.Now().UnixMilli()
	if err := rt.Memory.UpsertMemory(ctx, store.Memory{
		ID:          "mem-api-1",
		RoomID:      DefaultDebugRoomID,
		Kind:        store.MemoryKindSemantic,
		Content:     "调试者最喜欢拿铁咖啡",
		TEventMs:    nowMs,
		CreatedAtMs: nowMs,
		UpdatedAtMs: nowMs,
		Importance:  8,
		Entities:    []string{"调试者"},
		Embedding:   deterministicUnitVec("咖啡 拿铁", memory.DefaultVectorDim),
	}); err != nil {
		t.Fatalf("UpsertMemory: %v", err)
	}
	if _, err := rt.Emotion.ApplyStimulus(ctx, store.DefaultPersonaRoomID, "run-seed", persona.Stimulus{P: 0.4, A: 0.2, D: 0.1}, time.Now()); err != nil {
		t.Fatalf("ApplyStimulus: %v", err)
	}

	srv := httptest.NewServer(rt.Handler(nil))
	defer srv.Close()

	// 1. Chat Send + Timeline + Traces + Dashboard + PAD History
	respBody := doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/chat/send", map[string]any{
		"channel_id": "webui",
		"room_id":    DefaultDebugRoomID,
		"text":       "你好 101！",
	}, http.StatusAccepted)
	if !strings.Contains(respBody, `"accepted":true`) {
		t.Fatalf("chat send response = %s", respBody)
	}

	// Wait for the triggered debug run to complete.
	tr := waitForTraceStatus(t, rt.Store, DefaultDebugRoomID, string(StatusCompleted), 3*time.Second)

	var tlResp struct {
		Items []APITimelineMessage `json:"items"`
	}
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/chat/timeline?room_id="+DefaultDebugRoomID, nil, http.StatusOK), &tlResp)
	if len(tlResp.Items) < 2 {
		t.Fatalf("expected user + bot messages in timeline, got %d", len(tlResp.Items))
	}

	var tracesResp struct {
		Items []APIRunTraceSummary `json:"items"`
	}
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/traces?status=completed", nil, http.StatusOK), &tracesResp)
	if len(tracesResp.Items) == 0 {
		t.Fatal("expected at least 1 completed trace")
	}

	var traceDetail APIRunTraceDetail
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/traces/"+tr.RunID, nil, http.StatusOK), &traceDetail)
	if traceDetail.RunID != tr.RunID || len(traceDetail.Steps) == 0 {
		t.Fatalf("unexpected trace detail: %+v", traceDetail)
	}

	var dash APIDashboardResponse
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/dashboard", nil, http.StatusOK), &dash)
	if dash.Status != "ok" || dash.Metrics.MemoriesTotal != 1 || dash.Metrics.RunsToday < 1 || dash.Metrics.MessagesTotal < 2 {
		t.Fatalf("unexpected dashboard: %+v", dash)
	}
	if !slices.Contains(dash.Rooms, DefaultDebugRoomID) {
		t.Fatalf("expected dashboard rooms to include %q, got %v", DefaultDebugRoomID, dash.Rooms)
	}

	var padHist struct {
		Items []map[string]any `json:"items"`
	}
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/pad/history", nil, http.StatusOK), &padHist)
	if len(padHist.Items) == 0 {
		t.Fatal("expected non-empty pad history")
	}

	// Manual trigger endpoint
	doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/runs/trigger", map[string]any{
		"room_id":     DefaultDebugRoomID,
		"source":      "webui",
		"bypass_gate": true,
	}, http.StatusAccepted)

	// 2. Memories, Search, Aliases & Dreaming
	var memList struct {
		Items    []APIMemoryItem   `json:"items"`
		Dreaming APIDreamingStatus `json:"dreaming"`
	}
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/memories?room_id="+DefaultDebugRoomID, nil, http.StatusOK), &memList)
	if len(memList.Items) != 1 || memList.Items[0].Kind != "fact" {
		t.Fatalf("unexpected memory list: %+v", memList)
	}

	var memSearch APIMemorySearchResponse
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/memories/search?q=拿铁&room_id="+DefaultDebugRoomID, nil, http.StatusOK), &memSearch)
	if len(memSearch.Items) == 0 {
		t.Fatalf("expected search hit for 拿铁: %+v", memSearch)
	}

	doJSON(t, srv.Client(), http.MethodPut, srv.URL+"/api/memories/aliases", map[string]any{
		"room_id":          DefaultDebugRoomID,
		"alias":            "小调",
		"canonical_entity": "调试者",
		"sender_id":        "webui-user",
	}, http.StatusOK)
	var aliasList struct {
		Items []store.EntityAlias `json:"items"`
	}
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/memories/aliases?room_id="+DefaultDebugRoomID, nil, http.StatusOK), &aliasList)
	if len(aliasList.Items) != 1 || aliasList.Items[0].Alias != "小调" {
		t.Fatalf("unexpected aliases: %+v", aliasList.Items)
	}
	doJSON(t, srv.Client(), http.MethodDelete, srv.URL+"/api/memories/aliases?room_id="+DefaultDebugRoomID+"&alias=小调", nil, http.StatusOK)

	doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/memories/dream", map[string]any{"room_id": DefaultDebugRoomID}, http.StatusAccepted)

	// 3. Persona Core, Temperament, Reflect, Snapshots, Diff & Rollback
	var pResp APIPersonaResponse
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/persona", nil, http.StatusOK), &pResp)
	if pResp.CoreBeliefs == "" || len(pResp.Traits) != 9 || len(pResp.Snapshots) == 0 {
		t.Fatalf("unexpected initial persona: traits=%d snaps=%d", len(pResp.Traits), len(pResp.Snapshots))
	}
	initialSnapID := pResp.Snapshots[0].Version

	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodPatch, srv.URL+"/api/persona", map[string]any{
		"core_beliefs": "我是测试修改后的第一人称信念。",
	}, http.StatusOK), &pResp)
	if pResp.CoreBeliefs != "我是测试修改后的第一人称信念。" {
		t.Fatalf("beliefs did not update: %q", pResp.CoreBeliefs)
	}

	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodPut, srv.URL+"/api/persona/temperament", APIPADTemperament{
		InitialBaseline: pResp.Temperament.InitialBaseline,
		Baseline:        pResp.Temperament.Baseline,
		Sensitivity:     pResp.Temperament.Sensitivity,
		HalfLifePMs:     600_000,
		HalfLifeAMs:     300_000,
		HalfLifeDMs:     500_000,
		DeadzoneRadius:  0.20,
	}, http.StatusOK), &pResp)
	if pResp.Temperament.HalfLifePMs != 600_000 {
		t.Fatalf("temperament half_life_p_ms = %d, want 600000", pResp.Temperament.HalfLifePMs)
	}

	var reflectResp struct {
		Reflection persona.ReflectionResult `json:"reflection"`
		Persona    APIPersonaResponse       `json:"persona"`
	}
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/persona/reflect", map[string]any{
		"highlights": []string{"[2026-04-10] 大家在群里晒了好多可爱猫咪"},
	}, http.StatusOK), &reflectResp)
	if reflectResp.Reflection.NoChange || len(reflectResp.Persona.Traits) != 10 {
		t.Fatalf("expected reflection to add 10th trait, got %+v", reflectResp.Reflection)
	}
	reflectedSnapID := reflectResp.Reflection.SnapshotID

	var diff persona.SnapshotDiff
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet,
		srv.URL+"/api/persona/snapshots/diff?from="+itoa64(initialSnapID)+"&to="+itoa64(reflectedSnapID),
		nil, http.StatusOK), &diff)
	if !diff.BeliefsChanged || len(diff.AddedTraits) != 1 {
		t.Fatalf("unexpected snapshot diff: %+v", diff)
	}

	var rbResp struct {
		Rollback persona.RollbackResult `json:"rollback"`
		Persona  APIPersonaResponse     `json:"persona"`
	}
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodPost,
		srv.URL+"/api/persona/snapshots/"+itoa64(initialSnapID)+"/rollback",
		nil, http.StatusOK), &rbResp)
	if len(rbResp.Persona.Traits) != 9 {
		t.Fatalf("expected rollback to restore 9 traits, got %d", len(rbResp.Persona.Traits))
	}

	// Verify PUT /api/persona/traits endpoint used by Persona Studio.
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodPut, srv.URL+"/api/persona/traits", map[string]any{
		"traits": rbResp.Persona.Traits,
	}, http.StatusOK), &pResp)
	if len(pResp.Traits) != 9 {
		t.Fatalf("PUT /api/persona/traits count = %d, want 9", len(pResp.Traits))
	}

	// 4. Memes Upload, List, Binary Thumb/Image Serving, Update Impression & Delete
	var uploadBuf bytes.Buffer
	mw := multipart.NewWriter(&uploadBuf)
	fw, _ := mw.CreateFormFile("file", "cat.png")
	_, _ = fw.Write(makeTestPNG(t, color.RGBA{R: 120, G: 210, B: 180, A: 255}))
	_ = mw.WriteField("hint", "微笑猫猫")
	_ = mw.Close()

	upReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/memes", &uploadBuf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upRes, err := srv.Client().Do(upReq)
	if err != nil {
		t.Fatalf("upload meme: %v", err)
	}
	upRaw, _ := io.ReadAll(upRes.Body)
	_ = upRes.Body.Close()
	if upRes.StatusCode != http.StatusCreated {
		t.Fatalf("upload meme status = %d body = %s", upRes.StatusCode, string(upRaw))
	}
	var upPayload struct {
		Status string       `json:"status"`
		Item   *APIMemeItem `json:"item"`
	}
	unmarshalJSON(t, string(upRaw), &upPayload)
	if upPayload.Item == nil || upPayload.Item.Hash == "" {
		t.Fatalf("unexpected upload response: %s", string(upRaw))
	}
	memeHash := upPayload.Item.Hash

	var memeList APIMemeListResponse
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/memes", nil, http.StatusOK), &memeList)
	if memeList.Total != 1 || len(memeList.Items) != 1 {
		t.Fatalf("unexpected meme list: %+v", memeList)
	}

	for _, assetPath := range []string{upPayload.Item.ThumbURL, upPayload.Item.ImageURL} {
		assetRes, err := srv.Client().Get(srv.URL + assetPath)
		if err != nil {
			t.Fatalf("GET %s: %v", assetPath, err)
		}
		assetBytes, _ := io.ReadAll(assetRes.Body)
		_ = assetRes.Body.Close()
		if assetRes.StatusCode != http.StatusOK || len(assetBytes) == 0 || !strings.HasPrefix(assetRes.Header.Get("Content-Type"), "image/") {
			t.Fatalf("GET %s: status=%d ct=%q len=%d", assetPath, assetRes.StatusCode, assetRes.Header.Get("Content-Type"), len(assetBytes))
		}
	}

	var updatedMeme APIMemeItem
	unmarshalJSON(t, doJSON(t, srv.Client(), http.MethodPatch, srv.URL+"/api/memes/"+memeHash, map[string]any{
		"overlay_text": "哈哈哈笑死",
		"description":  "超开心大笑猫猫",
		"emotion_tags": []string{"开心", "大笑"},
		"scene_tags":   []string{"接梗"},
	}, http.StatusOK), &updatedMeme)
	if updatedMeme.OverlayText != "哈哈哈笑死" || updatedMeme.Description != "超开心大笑猫猫" || len(updatedMeme.EmotionTags) != 2 {
		t.Fatalf("unexpected updated meme: %+v", updatedMeme)
	}

	doJSON(t, srv.Client(), http.MethodDelete, srv.URL+"/api/memes/"+memeHash, nil, http.StatusOK)

	// 5. Runtime Config (Write-Only Secret Redaction + Atomic Hot-Reload Bash Toggle)
	const rawSecret = "AIzaSySUPER_SECRET_NEVER_LEAK_987654321"

	// Initially Bash tools must be OFF by default.
	regBefore, relBefore := rt.BuildRunTools(DefaultDebugRoomID)
	_, hasBashBefore := regBefore.Get("bash")
	relBefore()
	if hasBashBefore {
		t.Fatal("bash tool must be disabled by default")
	}

	patchRespRaw := doJSON(t, srv.Client(), http.MethodPatch, srv.URL+"/api/config", map[string]any{
		"providers": []map[string]any{
			{
				"id":         "google-main",
				"name":       "Google Gemini",
				"kind":       "google",
				"base_url":   "https://generativelanguage.googleapis.com",
				"secret_key": "custom-gemini-key",
				"api_key":    rawSecret,
				"enabled":    true,
			},
		},
		"model_slots": []map[string]any{
			{
				"slot":        "chat",
				"provider_id": "google-main",
				"model_id":    "gemini-2.5-flash",
				"capabilities": map[string]any{
					"vision": true,
				},
			},
		},
		"features": map[string]any{
			"memory_enabled":       true,
			"emotion_enabled":      true,
			"meme_collect_enabled": true,
			"bash_enabled":         true,
		},
	}, http.StatusOK)

	if strings.Contains(patchRespRaw, rawSecret) {
		t.Fatalf("CRITICAL: raw secret leaked in PATCH /api/config response: %s", patchRespRaw)
	}

	getCfgRaw := doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/config", nil, http.StatusOK)
	if strings.Contains(getCfgRaw, rawSecret) {
		t.Fatalf("CRITICAL: raw secret leaked in GET /api/config response: %s", getCfgRaw)
	}
	var cfgView APIRuntimeConfig
	unmarshalJSON(t, getCfgRaw, &cfgView)
	if len(cfgView.Providers) != 1 || cfgView.Providers[0].SecretKey != "custom-gemini-key" || !cfgView.Providers[0].HasAPIKey || !strings.HasPrefix(cfgView.Providers[0].APIKeyHint, "***") {
		t.Fatalf("expected redacted provider key metadata with custom secret_key, got %+v", cfgView.Providers)
	}
	if !cfgView.Features.BashEnabled {
		t.Fatalf("expected bash_enabled=true after config patch, got %+v", cfgView.Features)
	}

	// Verify hot-reload atomically updated both AI registry and Bash sandbox toggle!
	if resolved, err := rt.AIRegistry().ResolveSlot(ai.SlotChat); err != nil || resolved.Model.ModelID != "gemini-2.5-flash" {
		t.Fatalf("expected hot-reloaded chat slot gemini-2.5-flash, got %+v err=%v", resolved, err)
	}
	regAfter, relAfter := rt.BuildRunTools(DefaultDebugRoomID)
	_, hasBashAfter := regAfter.Get("bash")
	relAfter()
	if !hasBashAfter {
		t.Fatal("expected bash tool enabled after hot-reload")
	}

	// Explicit write-only secret endpoints must also never leak raw secrets.
	const extraSecret = "sk-live-DO_NOT_EXPOSURE_123456"
	putSecRaw := doJSON(t, srv.Client(), http.MethodPut, srv.URL+"/api/secrets/deepseek-api", map[string]any{
		"value": extraSecret,
	}, http.StatusOK)
	if strings.Contains(putSecRaw, extraSecret) {
		t.Fatalf("CRITICAL: raw secret leaked in PUT /api/secrets response: %s", putSecRaw)
	}
	listSecRaw := doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/secrets", nil, http.StatusOK)
	if strings.Contains(listSecRaw, extraSecret) || strings.Contains(listSecRaw, rawSecret) {
		t.Fatalf("CRITICAL: raw secret leaked in GET /api/secrets response: %s", listSecRaw)
	}
	doJSON(t, srv.Client(), http.MethodDelete, srv.URL+"/api/secrets/deepseek-api", nil, http.StatusOK)

	// Verify POST /api/mcp/{id}/reload endpoint used by Settings Studio.
	reloadRaw := doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/mcp/demo-mcp/reload", nil, http.StatusOK)
	if !strings.Contains(reloadRaw, `"reloaded":true`) {
		t.Fatalf("unexpected mcp reload response: %s", reloadRaw)
	}
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any, wantStatus int) string {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, url, err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	raw, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != wantStatus {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, url, res.StatusCode, wantStatus, string(raw))
	}
	return string(raw)
}

func unmarshalJSON(t *testing.T, raw string, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}
