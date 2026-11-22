package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"omni-bot/internal/ai"
	"omni-bot/internal/channel"
	"omni-bot/internal/meme"
	"omni-bot/internal/memory"
	"omni-bot/internal/persona"
	"omni-bot/internal/protocol"
	"omni-bot/internal/store"
	"omni-bot/internal/tools"
)

type stubLanguageModel struct {
	mu       sync.Mutex
	requests []ai.GenerateRequest
	fn       func(call int, req ai.GenerateRequest) (ai.GenerateResponse, error)
}

func (m *stubLanguageModel) Capabilities() ai.Capabilities {
	return ai.Capabilities{Vision: true, ToolUse: true}
}

func (m *stubLanguageModel) Generate(_ context.Context, req ai.GenerateRequest) (ai.GenerateResponse, error) {
	m.mu.Lock()
	idx := len(m.requests)
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	return m.fn(idx, req)
}

type stubDecisionModel struct {
	mu       sync.Mutex
	requests []ai.DecisionRequest
	fn       func(call int, req ai.DecisionRequest) (ai.DecisionResult, error)
}

func (m *stubDecisionModel) Decide(_ context.Context, req ai.DecisionRequest) (ai.DecisionResult, error) {
	m.mu.Lock()
	idx := len(m.requests)
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	return m.fn(idx, req)
}

type stubEmbeddingModel struct {
	calls atomic.Int32
	dim   int
}

func (m *stubEmbeddingModel) Embed(_ context.Context, req ai.EmbedRequest) (ai.EmbedResponse, error) {
	m.calls.Add(1)
	dim := req.Dimensions
	if dim <= 0 {
		dim = m.dim
	}
	if dim <= 0 {
		dim = memory.DefaultVectorDim
	}
	vecs := make([][]float32, len(req.Texts))
	for i, txt := range req.Texts {
		vecs[i] = deterministicUnitVec(txt, dim)
	}
	return ai.EmbedResponse{Vectors: vecs}, nil
}

type stubSpeechModel struct {
	calls atomic.Int32
}

func (m *stubSpeechModel) Synthesize(_ context.Context, req ai.SpeechRequest) (ai.SpeechResponse, error) {
	m.calls.Add(1)
	return ai.SpeechResponse{
		Audio:    []byte("FAKE-MP3-AUDIO:" + req.Text),
		MIMEType: "audio/mpeg",
	}, nil
}

func deterministicUnitVec(text string, dim int) []float32 {
	vec := make([]float32, dim)
	// Map semantic topics into consistent orthogonal buckets so deterministic tests have exact cosine similarity.
	switch {
	case strings.Contains(text, "显卡") || strings.Contains(text, "RTX") || strings.Contains(text, "4090"):
		vec[0] = 0.95
		vec[1] = 0.3122499
	case strings.Contains(text, "猫") || strings.Contains(text, "开心") || strings.Contains(text, "笑死"):
		vec[2] = 0.95
		vec[3] = 0.3122499
	default:
		vec[4] = 1.0
	}
	var sum float64
	for _, v := range vec {
		sum += float64(v * v)
	}
	if norm := float32(math.Sqrt(sum)); norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

func makeTestPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			if (x+y)%4 == 0 {
				img.Set(x, y, color.RGBA{R: 255 - c.R, G: 255 - c.G, B: 255 - c.B, A: 255})
			} else {
				img.Set(x, y, c)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestAgentLoop_RetryPolicyToolErrorsAndPrefixCacheInvariance(t *testing.T) {
	reg := tools.NewRegistry()
	if err := reg.Register(tools.FuncTool{
		Def: ai.ToolDef{Name: "failing_tool", Description: "always fails"},
		Fn: func(_ context.Context, _ []byte) (tools.Result, error) {
			return tools.Result{}, errors.New("boom from tool")
		},
	}); err != nil {
		t.Fatalf("register tool: %v", err)
	}

	draft := &ContextDraft{
		Tools:    reg,
		MaxSteps: MaxAgentSteps,
	}
	draft.AddStatic("STATIC_SYSTEM_PREFIX")
	draft.AddDynamic("INITIAL_USER_CONTEXT")
	spec := draft.Freeze()

	opaqueNative := json.RawMessage(`[{"type":"thought","signature":"sig-xyz"},{"type":"function_call","id":"c1"}]`)
	lm := &stubLanguageModel{
		fn: func(call int, req ai.GenerateRequest) (ai.GenerateResponse, error) {
			switch call {
			case 0:
				// First attempt fails with retryable transient error -> must retry once.
				return ai.GenerateResponse{}, &ai.Error{Kind: ai.ErrTransient, Message: "temporary 503"}
			case 1:
				return ai.GenerateResponse{
					ToolCalls: []ai.ToolCall{
						{ID: "c1", Name: "failing_tool", Arguments: map[string]any{"x": 1}},
						{ID: "c2", Name: "unknown_tool", Arguments: map[string]any{}},
					},
					NativeContent: opaqueNative,
					Usage:         ai.Usage{InputTokens: ai.IntPtr(100), OutputTokens: ai.IntPtr(20)},
				}, nil
			default:
				// Round 2 must receive error tool results and preserve exact system + tools + NativeContent.
				if req.System != "STATIC_SYSTEM_PREFIX" {
					t.Errorf("round 2 System mutated: %q", req.System)
				}
				if len(req.Tools) != 1 || req.Tools[0].Name != "failing_tool" {
					t.Errorf("round 2 Tools mutated: %+v", req.Tools)
				}
				if len(req.Messages) != 3 {
					t.Fatalf("round 2 Messages len = %d, want 3", len(req.Messages))
				}
				if string(req.Messages[1].NativeContent) != string(opaqueNative) {
					t.Errorf("opaque NativeContent not preserved: got %s", string(req.Messages[1].NativeContent))
				}
				if req.Messages[2].Role != ai.RoleTool || len(req.Messages[2].Parts) != 2 {
					t.Fatalf("unexpected tool result message: %+v", req.Messages[2])
				}
				if !req.Messages[2].Parts[0].ToolResult.IsError || !req.Messages[2].Parts[1].ToolResult.IsError {
					t.Errorf("expected tool failures converted into IsError results: %+v", req.Messages[2].Parts)
				}
				return ai.GenerateResponse{
					Text:  "已妥善处理工具错误",
					Usage: ai.Usage{InputTokens: ai.IntPtr(120), CachedTokens: ai.IntPtr(90), OutputTokens: ai.IntPtr(15)},
				}, nil
			}
		},
	}

	loop := NewAgentLoop(LoopOptions{MaxSteps: 6, RetryBackoff: time.Millisecond})
	rc := &RunContext{RunID: "run-retry-test", RoomID: "room-1"}
	if err := loop.Execute(t.Context(), rc, spec, lm); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rc.RawOutput != "已妥善处理工具错误" {
		t.Fatalf("RawOutput = %q", rc.RawOutput)
	}
	if rc.TotalUsage.CachedTokens != 90 {
		t.Fatalf("CachedTokens = %d, want 90", rc.TotalUsage.CachedTokens)
	}
	if len(lm.requests) != 3 {
		t.Fatalf("LLM calls = %d, want 3 (1 failed + 1 retry + 1 round2)", len(lm.requests))
	}

	// Non-retryable Auth error must fail immediately on first attempt without retry.
	authLM := &stubLanguageModel{
		fn: func(_ int, _ ai.GenerateRequest) (ai.GenerateResponse, error) {
			return ai.GenerateResponse{}, &ai.Error{Kind: ai.ErrAuth, StatusCode: 401, Message: "bad key"}
		},
	}
	err := loop.Execute(t.Context(), &RunContext{}, spec, authLM)
	if err == nil || ai.ErrorKindOf(err) != ai.ErrAuth {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
	if len(authLM.requests) != 1 {
		t.Fatalf("auth error was retried: calls = %d, want 1", len(authLM.requests))
	}

	// MaxSteps hard cap at 6 must abort infinite tool-call loops.
	infiniteLM := &stubLanguageModel{
		fn: func(call int, _ ai.GenerateRequest) (ai.GenerateResponse, error) {
			return ai.GenerateResponse{
				ToolCalls: []ai.ToolCall{{ID: "c", Name: "failing_tool"}},
			}, nil
		},
	}
	err = loop.Execute(t.Context(), &RunContext{}, spec, infiniteLM)
	if !errors.Is(err, ErrMaxStepsReached) {
		t.Fatalf("expected ErrMaxStepsReached, got %v", err)
	}
	if len(infiniteLM.requests) != MaxAgentSteps {
		t.Fatalf("infinite loop ran %d rounds, want hard cap %d", len(infiniteLM.requests), MaxAgentSteps)
	}
}

func TestKernelE2E_FullPipelineAndDreaming(t *testing.T) {
	ctx := t.Context()
	tmpDir := t.TempDir()

	st, err := store.Open(ctx, filepath.Join(tmpDir, "omni.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	blobs, err := store.NewBlobStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBlobStore: %v", err)
	}

	embedder := &stubEmbeddingModel{dim: memory.DefaultVectorDim}
	memEngine, err := memory.NewEngine(ctx, st, embedder, memory.Config{})
	if err != nil {
		t.Fatalf("memory.NewEngine: %v", err)
	}

	// Seed long-term memories (one active + one duplicate pair for dreaming + alias).
	nowMs := time.Now().UnixMilli()
	if err := st.UpsertEntityAlias(ctx, store.EntityAlias{
		RoomID:          "room-e2e",
		Alias:           "老王",
		CanonicalEntity: "王小明",
		SenderID:        "u-wang",
		UpdatedAtMs:     nowMs,
	}); err != nil {
		t.Fatalf("UpsertEntityAlias: %v", err)
	}
	gpuVec := deterministicUnitVec("显卡 RTX 4090", memory.DefaultVectorDim)
	if err := memEngine.UpsertMemory(ctx, store.Memory{
		ID:          "mem-gpu-1",
		RoomID:      "room-e2e",
		Kind:        store.MemoryKindSemantic,
		Content:     "王小明上个月刚换了 RTX 4090 显卡，打游戏帧率拉满",
		TEventMs:    nowMs - int64(3*24*time.Hour/time.Millisecond),
		CreatedAtMs: nowMs - int64(3*24*time.Hour/time.Millisecond),
		UpdatedAtMs: nowMs,
		Importance:  8,
		Entities:    []string{"王小明"},
		Embedding:   gpuVec,
	}); err != nil {
		t.Fatalf("UpsertMemory 1: %v", err)
	}
	// Near-duplicate episodic memory so RunDreaming collapses it into survivor.
	if err := memEngine.UpsertMemory(ctx, store.Memory{
		ID:          "mem-gpu-2",
		RoomID:      "room-e2e",
		Kind:        store.MemoryKindEpisodic,
		Content:     "王小明最近入手了 RTX 4090 新显卡",
		TEventMs:    nowMs - int64(2*24*time.Hour/time.Millisecond),
		CreatedAtMs: nowMs - int64(2*24*time.Hour/time.Millisecond),
		UpdatedAtMs: nowMs,
		Importance:  6,
		Entities:    []string{"王小明"},
		Embedding:   gpuVec,
	}); err != nil {
		t.Fatalf("UpsertMemory 2: %v", err)
	}

	emotion := persona.NewEmotionEngine(st)
	personaEng := persona.NewPersonaEngine(st, persona.WithEmotionEngine(emotion))
	if err := st.SetPersonaCore(ctx, store.PersonaCore{
		RoomID:  store.DefaultPersonaRoomID,
		Beliefs: persona.DefaultBeliefs(),
	}, "init"); err != nil {
		t.Fatalf("SetPersonaCore: %v", err)
	}
	if _, err := st.ReplacePersonaTraits(ctx, store.DefaultPersonaRoomID, persona.DefaultTraits(), "init"); err != nil {
		t.Fatalf("ReplacePersonaTraits: %v", err)
	}

	vault, err := meme.NewVault(meme.Options{
		Store: st,
		Blobs: blobs,
		Embedder: memeEmbedAdapter{
			embedder: embedder,
		},
	})
	if err != nil {
		t.Fatalf("meme.NewVault: %v", err)
	}
	ingestedMeme, err := vault.Ingest(ctx, makeTestPNG(t, color.RGBA{R: 255, G: 180, B: 60, A: 255}), ".png", "开心猫猫点赞庆祝 显卡")
	if err != nil || ingestedMeme.Status != meme.IngestStatusIngested {
		t.Fatalf("vault.Ingest: status=%q err=%v", ingestedMeme.Status, err)
	}
	shortMemeID := meme.ShortHash(ingestedMeme.Meme.Hash)

	decModel := &stubDecisionModel{
		fn: func(call int, req ai.DecisionRequest) (ai.DecisionResult, error) {
			if !strings.Contains(req.Input, "<latest_message>") {
				t.Errorf("decision input missing <latest_message>: %s", req.Input)
			}
			// Call 0: gate out (should_reply = 0.15) while still pushing positive PAD stimulus.
			if call == 0 {
				return ai.DecisionResult{
					Answers: map[string]ai.DecisionAnswer{
						persona.DecisionKeyShouldReply: {Kind: ai.DecisionNoul, Probability: 0.15, Confidence: 0.9},
						persona.DecisionKeyDeltaP:      {Kind: ai.DecisionScore, NormalizedScore: 0.6},
						persona.DecisionKeyDeltaA:      {Kind: ai.DecisionScore, NormalizedScore: 0.4},
						persona.DecisionKeyDeltaD:      {Kind: ai.DecisionScore, NormalizedScore: 0.2},
					},
					Usage: ai.Usage{InputTokens: ai.IntPtr(40)},
				}, nil
			}
			// Call 1: gate pass (should_reply = 0.92) + strong joyful stimulus.
			return ai.DecisionResult{
				Answers: map[string]ai.DecisionAnswer{
					persona.DecisionKeyShouldReply: {Kind: ai.DecisionNoul, Probability: 0.92, Confidence: 0.95},
					persona.DecisionKeyDeltaP:      {Kind: ai.DecisionScore, NormalizedScore: 0.9},
					persona.DecisionKeyDeltaA:      {Kind: ai.DecisionScore, NormalizedScore: 0.8},
					persona.DecisionKeyDeltaD:      {Kind: ai.DecisionScore, NormalizedScore: 0.5},
				},
				Usage: ai.Usage{InputTokens: ai.IntPtr(55)},
			}, nil
		},
	}

	speechModel := &stubSpeechModel{}
	chatOpaque := json.RawMessage(`[{"type":"thought","signature":"sig-e2e-1"},{"type":"function_call","id":"call-mem-1"}]`)
	chatModel := &stubLanguageModel{
		fn: func(call int, req ai.GenerateRequest) (ai.GenerateResponse, error) {
			if call == 0 {
				// Verify static + dynamic prompt assembly invariants.
				if !strings.Contains(req.System, "<system_protocol>") || !strings.Contains(req.System, "<self_beliefs>") || !strings.Contains(req.System, "<character_traits>") {
					t.Errorf("system prompt missing static layers: %s", req.System)
				}
				userTxt := req.Messages[0].Parts[0].Text
				if !strings.Contains(userTxt, "<recent_timeline>") || !strings.Contains(userTxt, "<associated_memories>") || !strings.Contains(userTxt, "<available_memes>") || !strings.Contains(userTxt, "<inner_feeling>") {
					t.Errorf("dynamic context missing required blocks:\n%s", userTxt)
				}
				return ai.GenerateResponse{
					ToolCalls: []ai.ToolCall{{
						ID:   "call-mem-1",
						Name: "memory_recall",
						Arguments: map[string]any{
							"query":  "RTX 4090",
							"entity": "老王",
						},
					}},
					NativeContent: chatOpaque,
					Usage:         ai.Usage{InputTokens: ai.IntPtr(300), OutputTokens: ai.IntPtr(35)},
				}, nil
			}
			// Round 2: assert memory_recall tool result returned real SQLite memory and emit multi-action outbound IR!
			toolResultText := req.Messages[2].Parts[0].ToolResult.Content
			if !strings.Contains(toolResultText, "RTX 4090") {
				t.Errorf("memory_recall tool result missing expected memory: %s", toolResultText)
			}
			return ai.GenerateResponse{
				Text:  "[reply:m-in-2] 记得呀，老王刚换了 4090！ ||| [meme:#" + shortMemeID + "] [voice:太强了吧]",
				Usage: ai.Usage{InputTokens: ai.IntPtr(360), CachedTokens: ai.IntPtr(280), OutputTokens: ai.IntPtr(40)},
			}, nil
		},
	}

	aiReg := ai.NewRegistry()
	_ = aiReg.BindSlot(ai.SlotChat, "chat-m")
	_ = aiReg.BindSlot(ai.SlotDecision, "dec-m")
	_ = aiReg.BindSlot(ai.SlotEmbedding, "emb-m")
	_ = aiReg.BindSlot(ai.SlotTTS, "tts-m")
	aiReg.SetLanguageModel("chat-m", chatModel)
	aiReg.SetDecisionModel("dec-m", decModel)
	aiReg.SetEmbeddingModel("emb-m", embedder)
	aiReg.SetSpeechModel("tts-m", speechModel)

	var pipe *Pipeline
	hub := channel.NewHub(channel.Options{
		Blobs: storeBlobAdapter{blobs: blobs},
		OnInbound: func(callCtx context.Context, msg protocol.InboundMessage) error {
			return pipe.HandleInbound(callCtx, msg)
		},
	})
	defer hub.Close()

	pipe, err = NewPipeline(PipelineConfig{
		Store:   st,
		Blobs:   blobs,
		Hub:     hub,
		AI:      aiReg,
		Memory:  memEngine,
		Emotion: emotion,
		Persona: personaEng,
		Memes:   vault,
		ToolsProvider: func(roomID string) (*tools.Registry, func()) {
			reg := tools.NewRegistry()
			_ = reg.Register(tools.NewMemoryRecallTool(&roomMemoryRecaller{engine: memEngine, roomID: roomID}))
			_ = reg.Register(tools.NewManageMemeTool(vault))
			return reg, func() {}
		},
	})
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	defer func() { _ = pipe.Close(t.Context()) }()

	srv := httptest.NewServer(hub.Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	// Connect WebUI WS subscriber.
	uiConn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial ui ws: %v", err)
	}
	defer func() { _ = uiConn.CloseNow() }()

	uiSubEnv, _ := protocol.NewRequest("ui-sub-1", protocol.MethodUISubscribe, protocol.UISubscribeParams{
		Topics: []string{protocol.EventPADUpdate, protocol.EventTraceStep, protocol.EventTimelineAppend},
	})
	uiSubFrame, _ := protocol.EncodeEnvelope(uiSubEnv)
	if err := uiConn.Write(ctx, websocket.MessageText, uiSubFrame); err != nil {
		t.Fatalf("ui subscribe write: %v", err)
	}
	_, _, err = uiConn.Read(ctx) // subscribe response
	if err != nil {
		t.Fatalf("ui subscribe ack: %v", err)
	}

	var (
		uiMu     sync.Mutex
		uiTopics = make(map[string]int)
	)
	uiReadCtx, uiCancel := context.WithCancel(ctx)
	defer uiCancel()
	go func() {
		for {
			_, data, err := uiConn.Read(uiReadCtx)
			if err != nil {
				return
			}
			env, err := protocol.DecodeEnvelope(data)
			if err != nil || env.Method != protocol.MethodUIEvent {
				continue
			}
			var ev protocol.UIEventParams
			if env.DecodeParams(&ev) == nil {
				uiMu.Lock()
				uiTopics[ev.Topic]++
				uiMu.Unlock()
			}
		}
	}()

	// Connect mock Chat Frontend Adapter over real WS.
	adConn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial adapter ws: %v", err)
	}
	defer func() { _ = adConn.CloseNow() }()

	regEnv, _ := protocol.NewRequest("ad-reg-1", protocol.MethodChannelRegister, protocol.ChannelRegisterParams{
		ChannelID: "napcat-e2e",
		Kind:      "napcat",
		Capabilities: []protocol.Capability{
			protocol.CapReply, protocol.CapReact, protocol.CapPoke,
			protocol.CapVoice, protocol.CapSticker, protocol.CapMultiBubble,
		},
	})
	regFrame, _ := protocol.EncodeEnvelope(regEnv)
	if err := adConn.Write(ctx, websocket.MessageText, regFrame); err != nil {
		t.Fatalf("adapter register write: %v", err)
	}
	_, _, err = adConn.Read(ctx) // register ack
	if err != nil {
		t.Fatalf("adapter register ack: %v", err)
	}

	deliveredCh := make(chan protocol.ChannelOutboundParams, 2)
	inboundAckCh := make(chan string, 4)
	adReadCtx, adCancel := context.WithCancel(ctx)
	defer adCancel()
	go func() {
		for {
			_, data, err := adConn.Read(adReadCtx)
			if err != nil {
				return
			}
			env, err := protocol.DecodeEnvelope(data)
			if err != nil {
				continue
			}
			if env.IsResponse() {
				inboundAckCh <- env.IDKey()
				continue
			}
			if env.Method == protocol.MethodChannelOutbound {
				var params protocol.ChannelOutboundParams
				if env.DecodeParams(&params) == nil {
					deliveredCh <- params
					respEnv, _ := protocol.NewResponse(env.ID, protocol.ChannelOutboundResult{
						Delivered: len(params.Actions),
						MsgIDs:    []string{"qq-out-101"},
					})
					respFrame, _ := protocol.EncodeEnvelope(respEnv)
					_ = adConn.Write(adReadCtx, websocket.MessageText, respFrame)
				}
			}
		}
	}()

	// Step 1: Send a message that gets gated out -> verify PAD still updates and trace is gated_out!
	in1, _ := protocol.NewRequest("in-1", protocol.MethodChannelInbound, protocol.InboundMessage{
		ChannelID:   "napcat-e2e",
		RoomID:      "room-e2e",
		MsgID:       "m-in-1",
		SenderID:    "u-wang",
		SenderName:  "老王",
		TimestampMs: time.Now().UnixMilli(),
		Blocks:      []protocol.ContentBlock{{Type: protocol.BlockText, Text: "早安大家"}},
	})
	in1Frame, _ := protocol.EncodeEnvelope(in1)
	if err := adConn.Write(ctx, websocket.MessageText, in1Frame); err != nil {
		t.Fatalf("send in-1: %v", err)
	}
	<-inboundAckCh

	waitForTraceStatus(t, st, "room-e2e", string(StatusGatedOut), 3*time.Second)

	padAfterGateOut, ok, err := st.GetPADState(ctx, "room-e2e")
	if err != nil || !ok {
		t.Fatalf("expected PAD state updated even on gated_out run: ok=%v err=%v", ok, err)
	}
	if padAfterGateOut.P <= 0.12 {
		t.Fatalf("expected PAD.P to increase above baseline 0.12 on gated_out stimulus, got %.4f", padAfterGateOut.P)
	}

	// Step 2: Send a message mentioning RTX 4090 -> triggers full 2-step tool loop + outbound IR!
	in2, _ := protocol.NewRequest("in-2", protocol.MethodChannelInbound, protocol.InboundMessage{
		ChannelID:   "napcat-e2e",
		RoomID:      "room-e2e",
		MsgID:       "m-in-2",
		SenderID:    "u-wang",
		SenderName:  "老王",
		IsAtBot:     true,
		TimestampMs: time.Now().UnixMilli(),
		Blocks:      []protocol.ContentBlock{{Type: protocol.BlockText, Text: "101还记得我换了什么显卡吗"}},
	})
	in2Frame, _ := protocol.EncodeEnvelope(in2)
	if err := adConn.Write(ctx, websocket.MessageText, in2Frame); err != nil {
		t.Fatalf("send in-2: %v", err)
	}
	<-inboundAckCh

	var outbound protocol.ChannelOutboundParams
	select {
	case outbound = <-deliveredCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for outbound IR delivery over WS")
	}

	if len(outbound.Actions) != 3 {
		t.Fatalf("outbound actions count = %d, want 3 (text + meme + voice): %+v", len(outbound.Actions), outbound.Actions)
	}
	if outbound.Actions[0].Type != protocol.ActionSendText || outbound.Actions[0].ReplyToMsgID != "m-in-2" {
		t.Fatalf("unexpected action[0]: %+v", outbound.Actions[0])
	}
	if outbound.Actions[1].Type != protocol.ActionSendMeme || outbound.Actions[1].Media == nil || outbound.Actions[1].Media.BlobID == "" {
		t.Fatalf("expected resolved meme action with BlobID, got %+v", outbound.Actions[1])
	}
	if outbound.Actions[2].Type != protocol.ActionSendVoice || outbound.Actions[2].Media == nil || outbound.Actions[2].Media.BlobID == "" {
		t.Fatalf("expected synthesized voice action with BlobID, got %+v", outbound.Actions[2])
	}
	if speechModel.calls.Load() != 1 {
		t.Fatalf("SpeechModel calls = %d, want 1", speechModel.calls.Load())
	}

	// Verify completed RunTrace persisted with tool calls, memory hits, PAD before/after, and actions.
	completedTrace := waitForTraceStatus(t, st, "room-e2e", string(StatusCompleted), 3*time.Second)
	var storedSteps StoredTraceSteps
	if err := json.Unmarshal([]byte(completedTrace.StepsJSON), &storedSteps); err != nil {
		t.Fatalf("unmarshal StepsJSON: %v", err)
	}
	if len(storedSteps.ToolCalls) != 1 || storedSteps.ToolCalls[0].Name != "memory_recall" {
		t.Fatalf("expected 1 memory_recall tool call in trace, got %+v", storedSteps.ToolCalls)
	}
	if storedSteps.Usage.CachedTokens != 280 {
		t.Fatalf("trace cached tokens = %d, want 280", storedSteps.Usage.CachedTokens)
	}

	// Verify bot reply written to timeline_messages and meme use_count incremented.
	timeline, err := st.ListRecentTimeline(ctx, "room-e2e", 10)
	if err != nil {
		t.Fatalf("ListRecentTimeline: %v", err)
	}
	var foundBotMsg bool
	for _, m := range timeline {
		if m.IsBot && strings.Contains(m.RenderedText, "4090") {
			foundBotMsg = true
			break
		}
	}
	if !foundBotMsg {
		t.Fatalf("expected bot reply persisted to timeline_messages: %+v", timeline)
	}

	updatedMeme, ok, err := st.GetMeme(ctx, ingestedMeme.Meme.Hash)
	if err != nil || !ok || updatedMeme.UseCount != 1 {
		t.Fatalf("expected meme use_count=1, got ok=%v count=%d err=%v", ok, updatedMeme.UseCount, err)
	}

	// Verify WebUI subscriber received pad.update, trace.step, and timeline.append events.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		uiMu.Lock()
		okTopics := uiTopics[protocol.EventPADUpdate] > 0 &&
			uiTopics[protocol.EventTraceStep] > 0 &&
			uiTopics[protocol.EventTimelineAppend] > 0
		uiMu.Unlock()
		if okTopics {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	uiMu.Lock()
	gotPAD, gotTrace, gotTL := uiTopics[protocol.EventPADUpdate], uiTopics[protocol.EventTraceStep], uiTopics[protocol.EventTimelineAppend]
	uiMu.Unlock()
	if gotPAD == 0 || gotTrace == 0 || gotTL == 0 {
		t.Fatalf("expected all UI WS topics received, got pad=%d trace=%d timeline=%d", gotPAD, gotTrace, gotTL)
	}

	// Verify Dreaming runs cleanly over the room's memories and collapses the duplicate GPU cluster.
	dreamReport, err := memEngine.RunDreaming(ctx, "room-e2e", memory.DreamingOptions{})
	if err != nil {
		t.Fatalf("RunDreaming: %v", err)
	}
	if dreamReport.MergedClusters < 1 || dreamReport.SupersededCount < 1 {
		t.Fatalf("expected dreaming to merge duplicate cluster, got %+v", dreamReport)
	}
}

func waitForTraceStatus(t *testing.T, st *store.Store, roomID, wantStatus string, timeout time.Duration) store.RunTrace {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		traces, err := st.ListRunTraces(t.Context(), roomID, 20)
		if err == nil {
			for _, tr := range traces {
				if tr.Status == wantStatus {
					return tr
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for run trace status %q in room %q", wantStatus, roomID)
	return store.RunTrace{}
}

type memeEmbedAdapter struct {
	embedder ai.EmbeddingModel
}

func (a memeEmbedAdapter) Embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := a.embedder.Embed(ctx, ai.EmbedRequest{
		Texts:      []string{text},
		Dimensions: memory.DefaultVectorDim,
	})
	if err != nil || len(resp.Vectors) == 0 {
		return nil, err
	}
	return resp.Vectors[0], nil
}

type roomMemoryRecaller struct {
	engine *memory.Engine
	roomID string
}

func (r *roomMemoryRecaller) Recall(ctx context.Context, q tools.MemoryRecallQuery) ([]tools.RecalledMemory, error) {
	res, err := r.engine.ActiveRecall(ctx, memory.ActiveRecallRequest{
		RoomID:          r.roomID,
		Query:           q.Query,
		Entity:          q.Entity,
		IncludeArchived: q.IncludeArchived,
		Limit:           q.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]tools.RecalledMemory, len(res.Hits))
	for i, h := range res.Hits {
		out[i] = tools.RecalledMemory{
			ID:        h.Memory.ID,
			Kind:      h.Memory.Kind,
			Content:   h.Memory.Content,
			TimeEvent: time.UnixMilli(h.Memory.TEventMs).UTC().Format("2006-01-02"),
			Entities:  h.Memory.Entities,
			Score:     h.Score,
			Archived:  h.Memory.Archived,
		}
	}
	return out, nil
}

type storeBlobAdapter struct {
	blobs *store.BlobStore
}

func (a storeBlobAdapter) Put(ctx context.Context, mimeType string, r io.Reader) (channel.BlobMeta, error) {
	m, err := a.blobs.PutBlob(ctx, r, mimeType)
	if err != nil {
		return channel.BlobMeta{}, err
	}
	return channel.BlobMeta{BlobID: m.BlobID, MIMEType: m.MIMEType, SizeBytes: m.SizeBytes}, nil
}

func (a storeBlobAdapter) Open(ctx context.Context, blobID string) (io.ReadCloser, channel.BlobMeta, error) {
	rc, m, err := a.blobs.GetBlob(ctx, blobID)
	if err != nil {
		if errors.Is(err, store.ErrBlobNotFound) {
			return nil, channel.BlobMeta{}, channel.ErrBlobNotFound
		}
		return nil, channel.BlobMeta{}, err
	}
	return rc, channel.BlobMeta{BlobID: m.BlobID, MIMEType: m.MIMEType, SizeBytes: m.SizeBytes}, nil
}
