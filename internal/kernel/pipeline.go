package kernel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/channel"
	"omni-bot/internal/meme"
	"omni-bot/internal/memory"
	"omni-bot/internal/persona"
	"omni-bot/internal/persona/pad"
	"omni-bot/internal/protocol"
	"omni-bot/internal/store"
	"omni-bot/internal/tools"
)

const (
	defaultBotName             = "bk101"
	defaultBotUserID           = "bot"
	defaultGateThreshold       = 0.50
	defaultRecentTimelineLimit = 30
	defaultDistillBatchSize    = 20
	defaultMemeHandLimit       = 6
)

// Static neutral system protocol physically isolated from the 1st-person persona self-mind.
const defaultSystemProtocol = `<system_protocol>
你正在参与一个熟人群聊对话。请直接以自然口语回复，如需触发平台动作，可在回复正文中直接使用以下轻量内联标记：
- [reply:<msg_id>]：引用回复某条历史消息；
- |||：将回复拆分为多条自然发送的连续气泡；
- [meme:#<id>]：发送 <available_memes> 手牌架中列出的对应表情包；
- [voice:<要说的话>]：发送一段语音消息；
- [react:<msg_id>:<emoji>]：对某条消息贴表情回应；
- [poke:<user_id>]：轻轻戳一戳某位群友；
- [silent]：决定本轮保持沉默，不发出任何消息。
若需查阅久远记忆、搜索网页或管理表情，请直接调用已注册的工具。严禁输出思考过程或伪造工具结果。
</system_protocol>`

// StoredTraceSteps is the structured JSON envelope stored inside store.RunTrace.StepsJSON.
type StoredTraceSteps struct {
	Steps     []TraceStep     `json:"steps"`
	ToolCalls []TraceToolCall `json:"tool_calls,omitempty"`
	Usage     UsageSummary    `json:"usage,omitempty"`
	Summary   string          `json:"summary,omitempty"`
}

// StoredTracePAD is the structured JSON envelope stored inside store.RunTrace.PADJSON.
type StoredTracePAD struct {
	Before         *pad.Vec3 `json:"pad_before,omitempty"`
	After          *pad.Vec3 `json:"pad_after,omitempty"`
	InnerFeeling   string    `json:"inner_feeling,omitempty"`
	PrototypeID    string    `json:"prototype_id,omitempty"`
	PrototypeLabel string    `json:"prototype_label,omitempty"`
}

// PipelineConfig wires the real underlying subsystems into the 3-stage Kernel Pipeline.
type PipelineConfig struct {
	Store               *store.Store
	Blobs               *store.BlobStore
	Hub                 *channel.Hub
	AI                  *ai.Registry
	AIGetter            func() *ai.Registry
	Memory              *memory.Engine
	Emotion             *persona.EmotionEngine
	Persona             *persona.PersonaEngine
	Memes               *meme.Vault
	ToolsProvider       func(roomID string) (*tools.Registry, func())
	FeatureFlags        func() store.FeatureFlags
	BehaviorConfig      func() store.BehaviorConfig
	BotName             string
	BotUserID           string
	GateThreshold       float64
	GateAtThreshold     float64
	RecentTimelineLimit int
	DistillBatchSize    int
	MaxSteps            int
	RetryBackoff        time.Duration
	DebounceWindow      time.Duration
	Location            *time.Location
	Clock               func() time.Time
	Logger              *slog.Logger
	Hooks               *HookChain
	DisableAsyncBg      bool
}

// Pipeline coordinates SingleFlightBus, 3-stage HookChain, <=6-round AgentLoop, and async maintenance.
type Pipeline struct {
	store               *store.Store
	blobs               *store.BlobStore
	hub                 *channel.Hub
	aiGetter            func() *ai.Registry
	memory              *memory.Engine
	emotion             *persona.EmotionEngine
	persona             *persona.PersonaEngine
	memes               *meme.Vault
	toolsProvider       func(roomID string) (*tools.Registry, func())
	featureFlags        func() store.FeatureFlags
	behaviorConfig      func() store.BehaviorConfig
	botName             string
	botUserID           string
	gateThreshold       float64
	gateAtThreshold     float64
	recentTimelineLimit int
	distillBatchSize    int
	location            *time.Location
	clock               func() time.Time
	logger              *slog.Logger
	disableAsyncBg      bool

	hooks *HookChain
	loop  *AgentLoop
	bus   *SingleFlightBus

	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgWG     sync.WaitGroup

	// distillMu deduplicates concurrent background distillation triggers per room without blocking hot path.
	distillMu     sync.Mutex
	distillActive map[string]bool
}

// NewPipeline creates a wired Kernel Pipeline with default 3-stage hooks and a SingleFlightBus.
func NewPipeline(cfg PipelineConfig) (*Pipeline, error) {
	if cfg.Store == nil {
		return nil, errors.New("kernel: store is required")
	}

	botName := strings.TrimSpace(cfg.BotName)
	if botName == "" {
		botName = defaultBotName
	}
	botUserID := strings.TrimSpace(cfg.BotUserID)
	if botUserID == "" {
		botUserID = defaultBotUserID
	}
	gateThreshold := cfg.GateThreshold
	if gateThreshold <= 0 {
		gateThreshold = defaultGateThreshold
	}
	gateAtThreshold := cfg.GateAtThreshold
	if gateAtThreshold <= 0 {
		gateAtThreshold = min(0.30, gateThreshold)
	}
	recentLimit := cfg.RecentTimelineLimit
	if recentLimit <= 0 {
		recentLimit = defaultRecentTimelineLimit
	}
	distillBatch := cfg.DistillBatchSize
	if distillBatch <= 0 {
		distillBatch = defaultDistillBatchSize
	}
	loc := cfg.Location
	if loc == nil {
		loc = time.Local
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	aiGetter := cfg.AIGetter
	if aiGetter == nil {
		staticAI := cfg.AI
		aiGetter = func() *ai.Registry { return staticAI }
	}

	flagsFn := cfg.FeatureFlags
	if flagsFn == nil {
		flagsFn = store.DefaultFeatureFlags
	}

	bgCtx, bgCancel := context.WithCancel(context.Background())

	p := &Pipeline{
		store:               cfg.Store,
		blobs:               cfg.Blobs,
		hub:                 cfg.Hub,
		aiGetter:            aiGetter,
		memory:              cfg.Memory,
		emotion:             cfg.Emotion,
		persona:             cfg.Persona,
		memes:               cfg.Memes,
		toolsProvider:       cfg.ToolsProvider,
		featureFlags:        flagsFn,
		behaviorConfig:      cfg.BehaviorConfig,
		botName:             botName,
		botUserID:           botUserID,
		gateThreshold:       gateThreshold,
		gateAtThreshold:     gateAtThreshold,
		recentTimelineLimit: recentLimit,
		distillBatchSize:    distillBatch,
		location:            loc,
		clock:               clock,
		logger:              logger.With("component", "kernel.pipeline"),
		disableAsyncBg:      cfg.DisableAsyncBg,
		bgCtx:               bgCtx,
		bgCancel:            bgCancel,
		distillActive:       make(map[string]bool),
	}

	p.loop = NewAgentLoop(LoopOptions{
		MaxSteps:     cfg.MaxSteps,
		RetryBackoff: cfg.RetryBackoff,
		OnStep: func(rc *RunContext, step TraceStep) {
			p.broadcastTraceStep(rc, step.Stage, &step)
		},
	})

	if cfg.Hooks != nil {
		p.hooks = cfg.Hooks
	} else {
		p.hooks = p.buildDefaultHooks()
	}

	bus, err := NewSingleFlightBus(p.ExecuteTrigger, BusOptions{
		DebounceWindow: cfg.DebounceWindow,
		Logger:         p.logger,
	})
	if err != nil {
		bgCancel()
		return nil, err
	}
	p.bus = bus
	return p, nil
}

// Bus exposes the underlying per-room SingleFlightBus.
func (p *Pipeline) Bus() *SingleFlightBus { return p.bus }

// Hooks exposes the HookChain so callers can attach custom extension hooks.
func (p *Pipeline) Hooks() *HookChain { return p.hooks }

// Close gracefully stops the bus and waits for in-flight Runs and background maintenance tasks.
func (p *Pipeline) Close(ctx context.Context) error {
	busErr := p.bus.Close(ctx)
	p.bgCancel()

	done := make(chan struct{})
	go func() {
		p.bgWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		return busErr
	case <-ctx.Done():
		return errors.Join(busErr, ctx.Err())
	}
}

// HandleInbound persists an incoming message immediately to SQLite, broadcasts timeline.append,
// and submits a message trigger to the room's SingleFlightBus without holding any lock.
func (p *Pipeline) HandleInbound(ctx context.Context, msg protocol.InboundMessage) error {
	if strings.TrimSpace(msg.RoomID) == "" {
		return ErrEmptyRoomID
	}
	if msg.TimestampMs == 0 {
		msg.TimestampMs = p.clock().UnixMilli()
	}
	if msg.MsgID == "" {
		msg.MsgID = "msg-" + randomHex(6)
	}

	blocksJSON, err := json.Marshal(msg.Blocks)
	if err != nil {
		return fmt.Errorf("kernel: marshal inbound blocks: %w", err)
	}
	rendered := RenderBlocksText(msg.Blocks)

	row := store.TimelineMessage{
		ChannelID:    msg.ChannelID,
		RoomID:       msg.RoomID,
		MsgID:        msg.MsgID,
		SenderID:     msg.SenderID,
		SenderName:   msg.SenderName,
		IsBot:        msg.IsBot,
		IsAtBot:      msg.IsAtBot,
		TimestampMs:  msg.TimestampMs,
		ContentJSON:  string(blocksJSON),
		RenderedText: rendered,
		Status:       "UNPROCESSED",
	}
	if _, err := p.store.AppendTimelineMessage(ctx, row); err != nil {
		return fmt.Errorf("kernel: persist inbound timeline: %w", err)
	}

	p.broadcastTimelineAppend(msg)

	if msg.IsBot {
		return nil
	}

	return p.bus.Submit(TriggerEvent{
		Source:     TriggerMessage,
		ChannelID:  msg.ChannelID,
		RoomID:     msg.RoomID,
		BypassGate: false,
		// Use local kernel arrival time so remote platform timestamp skew never short-circuits debounce.
		ReceivedAt: p.clock(),
	})
}

// ExecuteTrigger executes a full 3-stage Run for a single TriggerEvent on the room's serial lane.
func (p *Pipeline) ExecuteTrigger(ctx context.Context, ev TriggerEvent) error {
	rc := &RunContext{
		RunID:     "run-" + randomHex(8),
		RoomID:    strings.TrimSpace(ev.RoomID),
		ChannelID: strings.TrimSpace(ev.ChannelID),
		Trigger:   ev,
		StartedAt: p.clock(),
		Status:    StatusRunning,
	}
	defer func() {
		if rc.ReleaseTools != nil {
			rc.ReleaseTools()
			rc.ReleaseTools = nil
		}
	}()

	// Persist initial running trace so observers can inspect active Runs.
	p.persistRunTrace(ctx, rc)

	// Stage 1: OnGate
	p.broadcastTraceStep(rc, StageGate, nil)
	if err := p.hooks.RunGate(ctx, rc); err != nil {
		rc.Status = StatusError
		rc.Error = err.Error()
		rc.FinishedAt = p.clock()
		_ = p.hooks.RunPostRun(ctx, rc)
		return err
	}

	if !rc.Gate.ShouldReply {
		rc.Status = StatusGatedOut
		rc.FinishedAt = p.clock()
		if err := p.hooks.RunPostRun(ctx, rc); err != nil {
			return err
		}
		return nil
	}

	// Stage 2: OnAssemble
	p.broadcastTraceStep(rc, StageAssemble, nil)
	draft := &ContextDraft{
		MaxSteps: MaxAgentSteps,
	}
	if p.toolsProvider != nil {
		reg, release := p.toolsProvider(rc.RoomID)
		draft.Tools = reg
		rc.ReleaseTools = release
	}

	asmT0 := time.Now()
	spec, err := p.hooks.RunAssemble(ctx, rc, draft)
	asmStep := rc.AppendStep(TraceStep{
		Stage:      StageAssemble,
		Name:       "assemble_context",
		Status:     statusFromErr(err),
		DurationMs: time.Since(asmT0).Milliseconds(),
		Detail: map[string]any{
			"memory_hits": len(rc.MemoryHits),
			"hand_memes":  len(rc.HandMemes),
			"tools_count": len(draftToolDefs(spec)),
		},
	})
	p.broadcastTraceStep(rc, StageAssemble, &asmStep)
	if err != nil {
		rc.Status = StatusError
		rc.Error = err.Error()
		rc.FinishedAt = p.clock()
		_ = p.hooks.RunPostRun(ctx, rc)
		return err
	}

	// Stage 3: Agent Loop (<= 6 rounds)
	p.broadcastTraceStep(rc, StageLoop, nil)
	chatModel, err := p.resolveChatModel()
	if err != nil {
		rc.Status = StatusError
		rc.Error = err.Error()
		rc.FinishedAt = p.clock()
		_ = p.hooks.RunPostRun(ctx, rc)
		return err
	}

	if err := p.loop.Execute(ctx, rc, spec, chatModel); err != nil {
		rc.Status = StatusError
		rc.Error = err.Error()
		rc.FinishedAt = p.clock()
		_ = p.hooks.RunPostRun(ctx, rc)
		return err
	}

	// Stage 4: OnPostRun
	p.broadcastTraceStep(rc, StagePostRun, nil)
	rc.FinishedAt = p.clock()
	if err := p.hooks.RunPostRun(ctx, rc); err != nil {
		return err
	}
	return nil
}

func (p *Pipeline) buildDefaultHooks() *HookChain {
	chain := NewHookChain()

	chain.AddGate(GateHookFunc{
		HookName: "default_gate_and_pad",
		Fn:       p.defaultOnGate,
	})

	chain.AddContributor(ContextContributorFunc{
		HookName: "system_protocol",
		Order:    10,
		Fn: func(_ context.Context, _ *RunContext, draft *ContextDraft) error {
			draft.AddStatic(defaultSystemProtocol)
			return nil
		},
	})
	chain.AddContributor(ContextContributorFunc{
		HookName: "persona_static",
		Order:    20,
		Fn:       p.contributePersonaStatic,
	})
	chain.AddContributor(ContextContributorFunc{
		HookName: "timeline_context",
		Order:    30,
		Fn:       p.contributeTimeline,
	})
	chain.AddContributor(ContextContributorFunc{
		HookName: "inner_feeling",
		Order:    40,
		Fn:       p.contributeInnerFeeling,
	})
	chain.AddContributor(ContextContributorFunc{
		HookName: "associated_memories",
		Order:    50,
		Fn:       p.contributeMemories,
	})
	chain.AddContributor(ContextContributorFunc{
		HookName: "available_memes",
		Order:    60,
		Fn:       p.contributeMemes,
	})

	chain.AddPostRun(PostRunHookFunc{
		HookName: "default_post_run",
		Fn:       p.defaultOnPostRun,
	})
	return chain
}

func (p *Pipeline) defaultOnGate(ctx context.Context, rc *RunContext) error {
	t0 := time.Now()
	flags := p.featureFlags()

	recent, err := p.store.ListRecentTimeline(ctx, rc.RoomID, p.recentTimelineLimit)
	if err != nil {
		return fmt.Errorf("load recent timeline: %w", err)
	}
	rc.RecentMessages = recent

	var (
		earlierMsgs []store.TimelineMessage
		pendingMsgs []store.TimelineMessage
		isAtBot     bool
	)
	for _, m := range recent {
		if m.Status == "UNPROCESSED" && !m.IsBot {
			pendingMsgs = append(pendingMsgs, m)
			if m.IsAtBot {
				isAtBot = true
			}
		} else {
			earlierMsgs = append(earlierMsgs, m)
		}
	}
	if len(pendingMsgs) == 0 {
		if rc.Trigger.Source == TriggerMessage && !rc.Trigger.BypassGate {
			// Nothing unprocessed left (already coalesced by an earlier run); skip with zero LLM calls.
			rc.Gate.ShouldReply = false
			rc.Gate.Reason = "no_unprocessed"
			return nil
		}
		if len(recent) > 0 {
			// Manual/scheduled trigger with no unread messages: treat the tail message as latest context.
			last := recent[len(recent)-1]
			pendingMsgs = []store.TimelineMessage{last}
			if len(earlierMsgs) > 0 && earlierMsgs[len(earlierMsgs)-1].SeqID == last.SeqID {
				earlierMsgs = earlierMsgs[:len(earlierMsgs)-1]
			}
			isAtBot = last.IsAtBot
		}
	}
	rc.PendingMessages = pendingMsgs

	// Infer ChannelID from pending/recent timeline when trigger omitted it.
	if rc.ChannelID == "" {
		for i := len(pendingMsgs) - 1; i >= 0; i-- {
			if pendingMsgs[i].ChannelID != "" {
				rc.ChannelID = pendingMsgs[i].ChannelID
				break
			}
		}
		if rc.ChannelID == "" {
			for i := len(recent) - 1; i >= 0; i-- {
				if recent[i].ChannelID != "" {
					rc.ChannelID = recent[i].ChannelID
					break
				}
			}
		}
	}

	botName := p.resolveBotName()
	earlierLines := formatTimelineLines(earlierMsgs, botName)
	latestLines := formatTimelineLines(pendingMsgs, botName)
	queryText := buildQueryText(pendingMsgs)
	rc.Gate.QueryText = queryText

	var (
		decModel   ai.DecisionModel
		embedModel ai.EmbeddingModel
	)
	if reg := p.aiGetter(); reg != nil {
		if dm, err := reg.Decision(ai.SlotDecision); err == nil {
			decModel = dm
		}
		if em, err := reg.Embedding(ai.SlotEmbedding); err == nil {
			embedModel = em
		}
	}

	var (
		decReq    ai.DecisionRequest
		decState  pad.State
		hasDecReq bool
		decRes    ai.DecisionResult
		decErr    error
		queryVec  []float32
		embedErr  error
	)

	if decModel != nil && p.emotion != nil && len(latestLines) > 0 {
		req, st, err := p.emotion.BuildGateDecisionRequest(ctx, rc.RoomID, earlierLines, latestLines, isAtBot)
		if err != nil {
			return fmt.Errorf("build gate decision request: %w", err)
		}
		decReq = req
		decState = st
		hasDecReq = true
		before := st.Value
		rc.Gate.PADBefore = &before
	}

	// Execute DecisionModel.Decide and 1024d EmbeddingModel.Embed CONCURRENTLY.
	var wg sync.WaitGroup
	if hasDecReq {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decRes, decErr = decModel.Decide(ctx, decReq)
		}()
	}

	if queryText != "" && (flags.MemoryEnabled || p.memes != nil) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.memory != nil {
				queryVec, embedErr = p.memory.EmbedQuery(ctx, queryText)
			} else if embedModel != nil {
				resp, err := embedModel.Embed(ctx, ai.EmbedRequest{
					Texts:      []string{queryText},
					Dimensions: memory.DefaultVectorDim,
				})
				if err != nil {
					embedErr = err
				} else if len(resp.Vectors) > 0 {
					queryVec = resp.Vectors[0]
				}
			}
		}()
	}
	wg.Wait()

	if embedErr != nil {
		p.logger.Warn("concurrent query embedding failed; continuing with lexical recall",
			slog.String("room_id", rc.RoomID),
			slog.String("run_id", rc.RunID),
			slog.String("err", embedErr.Error()),
		)
	} else {
		rc.Gate.QueryVector = queryVec
	}

	// Mark pending messages processed now that this Run has consumed them.
	if len(pendingMsgs) > 0 {
		seqIDs := make([]int64, 0, len(pendingMsgs))
		for _, m := range pendingMsgs {
			if m.SeqID > 0 && m.Status == "UNPROCESSED" {
				seqIDs = append(seqIDs, m.SeqID)
			}
		}
		if len(seqIDs) > 0 {
			if err := p.store.UpdateTimelineStatus(ctx, seqIDs, "PROCESSED"); err != nil {
				p.logger.Warn("failed to mark timeline messages processed", "room_id", rc.RoomID, "err", err)
			}
		}
	}

	if hasDecReq {
		if decErr != nil {
			if !rc.Trigger.BypassGate {
				return fmt.Errorf("decision gate failed: %w", decErr)
			}
			p.logger.Warn("decision model failed on bypassed trigger; proceeding", "room_id", rc.RoomID, "err", decErr)
		} else {
			rc.Gate.Usage = decRes.Usage
			rc.TotalUsage.Add(decRes.Usage)

			outcome, err := persona.ParseGateDecision(decRes)
			if err != nil {
				if !rc.Trigger.BypassGate {
					return fmt.Errorf("parse gate decision: %w", err)
				}
				p.logger.Warn("parse gate decision failed on bypassed trigger", "room_id", rc.RoomID, "err", err)
			} else {
				rc.Gate.Score = outcome.ShouldReply
				rc.Gate.Confidence = outcome.ShouldReplyConfidence

				// Update continuous PAD state unconditionally (even when gate skips replying).
				if flags.EmotionEnabled && p.emotion != nil {
					trans, err := p.emotion.ApplyStimulus(ctx, rc.RoomID, rc.RunID, outcome.Stimulus, p.clock())
					if err != nil {
						p.logger.Warn("failed to apply PAD stimulus", "room_id", rc.RoomID, "err", err)
					} else {
						oldV := trans.OldPAD
						newV := trans.NewPAD
						rc.Gate.PADBefore = &oldV
						rc.Gate.PADAfter = &newV
						rc.Gate.InnerFeeling = trans.InnerFeeling
						rc.Gate.PrototypeID = trans.PrototypeID
						rc.Gate.PrototypeLabel = trans.PrototypeLabel
						p.broadcastPADUpdate(rc.RoomID, trans)
					}
				} else {
					after := decState.Value
					rc.Gate.PADAfter = &after
				}

				if rc.Trigger.BypassGate {
					rc.Gate.ShouldReply = true
					rc.Gate.Bypassed = true
					rc.Gate.Reason = "bypass_gate"
				} else {
					// Pure threshold + prompt gate loaded dynamically from SQLite config (NO cooldown, NO PAD tampering).
					threshold, atThreshold := p.resolveGateThresholds()
					if isAtBot {
						threshold = atThreshold
					}
					if outcome.ShouldReply >= threshold {
						rc.Gate.ShouldReply = true
						rc.Gate.Reason = "decision_pass"
					} else {
						rc.Gate.ShouldReply = false
						rc.Gate.Reason = "decision_skip"
					}
				}
			}
		}
	} else {
		// No DecisionModel configured or no latest message text.
		if rc.Trigger.BypassGate || decModel == nil {
			rc.Gate.ShouldReply = true
			rc.Gate.Bypassed = rc.Trigger.BypassGate
			rc.Gate.Score = 1.0
			rc.Gate.Reason = "default_pass"
		}
		if flags.EmotionEnabled && p.emotion != nil {
			if st, err := p.emotion.CurrentFeeling(ctx, rc.RoomID, p.clock()); err == nil {
				v := st.State.Value
				rc.Gate.PADBefore = &v
				rc.Gate.PADAfter = &v
				rc.Gate.InnerFeeling = st.InnerFeeling
				rc.Gate.PrototypeID = st.PrototypeID
				rc.Gate.PrototypeLabel = st.PrototypeLabel
			}
		}
	}

	stepStatus := "ok"
	if !rc.Gate.ShouldReply {
		stepStatus = "skipped"
	}
	step := rc.AppendStep(TraceStep{
		Stage:      StageGate,
		Name:       "decision_gate",
		Status:     stepStatus,
		DurationMs: time.Since(t0).Milliseconds(),
		TokensIn:   derefInt(rc.Gate.Usage.InputTokens),
		Detail: map[string]any{
			"should_reply": rc.Gate.ShouldReply,
			"score":        rc.Gate.Score,
			"bypassed":     rc.Gate.Bypassed,
			"has_vec":      len(rc.Gate.QueryVector) > 0,
			"reason":       rc.Gate.Reason,
		},
	})
	p.broadcastTraceStep(rc, StageGate, &step)
	return nil
}

func (p *Pipeline) contributePersonaStatic(ctx context.Context, rc *RunContext, draft *ContextDraft) error {
	if p.persona == nil {
		return nil
	}
	blocks, err := p.persona.AssemblePromptBlocks(ctx, rc.RoomID, p.clock())
	if err != nil {
		return fmt.Errorf("assemble persona blocks: %w", err)
	}
	draft.AddStatic(blocks.StaticSystemBlock())
	if rc.Gate.InnerFeeling == "" && blocks.PADStatus.Active {
		rc.Gate.InnerFeeling = blocks.PADStatus.InnerFeeling
	}
	return nil
}

func (p *Pipeline) contributeTimeline(ctx context.Context, rc *RunContext, draft *ContextDraft) error {
	if summary, ok, err := p.store.GetRoomSummary(ctx, rc.RoomID); err == nil && ok && strings.TrimSpace(summary.SummaryText) != "" {
		draft.AddDynamic("<rolling_summary>\n" + strings.TrimSpace(summary.SummaryText) + "\n</rolling_summary>")
	}

	recent := rc.RecentMessages
	if len(recent) == 0 {
		var err error
		recent, err = p.store.ListRecentTimeline(ctx, rc.RoomID, p.recentTimelineLimit)
		if err != nil {
			return fmt.Errorf("list timeline: %w", err)
		}
		rc.RecentMessages = recent
	}

	if len(recent) > 0 {
		botName := p.resolveBotName()
		var sb strings.Builder
		sb.WriteString("<recent_timeline>\n")
		for _, m := range recent {
			ts := time.UnixMilli(m.TimestampMs).In(p.location).Format("15:04")
			speaker := formatSpeakerLabel(m, botName)
			if m.MsgID != "" {
				fmt.Fprintf(&sb, "[%s] (id:%s) %s: %s\n", ts, m.MsgID, speaker, strings.TrimSpace(m.RenderedText))
			} else {
				fmt.Fprintf(&sb, "[%s] %s: %s\n", ts, speaker, strings.TrimSpace(m.RenderedText))
			}
		}
		sb.WriteString("</recent_timeline>")
		draft.AddDynamic(sb.String())
	}

	// Attach multimodal media parts from pending messages for native or MediaFallback consumption.
	for _, m := range rc.PendingMessages {
		if m.ContentJSON == "" || m.ContentJSON == "[]" {
			continue
		}
		var blocks []protocol.ContentBlock
		if err := json.Unmarshal([]byte(m.ContentJSON), &blocks); err != nil {
			continue
		}
		for _, blk := range blocks {
			if blk.Media == nil || blk.Media.BlobID == "" {
				continue
			}
			var kind ai.PartKind
			switch blk.Type {
			case protocol.BlockImage:
				kind = ai.PartImage
			case protocol.BlockAudio:
				kind = ai.PartAudio
			case protocol.BlockVideo:
				kind = ai.PartVideo
			default:
				continue
			}
			draft.MediaParts = append(draft.MediaParts, ai.MediaPart(kind, ai.MediaRef{
				BlobID:     blk.Media.BlobID,
				MIMEType:   blk.Media.MIMEType,
				SizeBytes:  blk.Media.SizeBytes,
				DurationMs: blk.Media.DurationMs,
				Caption:    blk.Description,
			}))
		}
	}
	return nil
}

func (p *Pipeline) contributeInnerFeeling(_ context.Context, rc *RunContext, draft *ContextDraft) error {
	if !p.featureFlags().EmotionEnabled {
		return nil
	}
	// Strictly omit <inner_feeling> when empty (neutral deadzone).
	if block := persona.RenderInnerFeelingBlock(rc.Gate.InnerFeeling); block != "" {
		draft.AddDynamic(block)
	}
	return nil
}

func (p *Pipeline) contributeMemories(ctx context.Context, rc *RunContext, draft *ContextDraft) error {
	if p.memory == nil || !p.featureFlags().MemoryEnabled {
		return nil
	}
	query := strings.TrimSpace(rc.Gate.QueryText)
	if query == "" && len(rc.Gate.QueryVector) == 0 {
		return nil
	}

	var speaker, speakerID string
	if len(rc.PendingMessages) > 0 {
		last := rc.PendingMessages[len(rc.PendingMessages)-1]
		speaker = last.SenderName
		speakerID = last.SenderID
	}

	recentWindow := make([]string, 0, len(rc.RecentMessages))
	for _, m := range rc.RecentMessages {
		if txt := strings.TrimSpace(m.RenderedText); txt != "" {
			recentWindow = append(recentWindow, txt)
		}
	}

	res, err := p.memory.PassiveRecall(ctx, memory.PassiveRecallRequest{
		RoomID:       rc.RoomID,
		Speaker:      speaker,
		SpeakerID:    speakerID,
		Query:        query,
		QueryVector:  rc.Gate.QueryVector,
		RecentWindow: recentWindow,
		NowMs:        p.clock().UnixMilli(),
	})
	if err != nil {
		p.logger.Warn("passive memory recall failed; continuing without memories",
			slog.String("room_id", rc.RoomID),
			slog.String("err", err.Error()),
		)
		return nil
	}
	rc.MemoryHits = res.Hits
	rc.MemoryTrace = res.Trace
	if res.FormattedBlock != "" {
		draft.AddDynamic(res.FormattedBlock)
	}
	return nil
}

func (p *Pipeline) contributeMemes(ctx context.Context, rc *RunContext, draft *ContextDraft) error {
	if p.memes == nil {
		return nil
	}
	items, block, err := p.memes.RetrieveHand(ctx, rc.Gate.QueryText, rc.Gate.QueryVector, defaultMemeHandLimit)
	if err != nil {
		p.logger.Warn("meme hand retrieval failed; continuing without memes",
			slog.String("room_id", rc.RoomID),
			slog.String("err", err.Error()),
		)
		return nil
	}
	rc.HandMemes = items
	if block != "" {
		draft.AddDynamic(block)
	}
	return nil
}

func (p *Pipeline) defaultOnPostRun(ctx context.Context, rc *RunContext) error {
	t0 := time.Now()
	botReplied := false

	if rc.Status == StatusRunning {
		actions, silent := protocol.ParseOutbound(rc.RawOutput)
		rc.Silent = silent
		if silent || len(actions) == 0 {
			rc.Status = StatusSilent
		} else {
			enriched := p.enrichOutboundActions(ctx, actions)
			rc.Actions = enriched

			if p.hub != nil && rc.ChannelID != "" && p.hub.HasAdapter(rc.ChannelID) {
				res, err := p.hub.SendOutbound(ctx, rc.ChannelID, rc.RoomID, enriched)
				if err != nil {
					rc.Status = StatusError
					rc.Error = fmt.Sprintf("deliver outbound: %v", err)
				} else {
					rc.DeliveredMsgIDs = res.MsgIDs
					rc.Status = StatusCompleted
					botReplied = true
				}
			} else {
				// No external WS adapter connected for this channel (e.g. built-in WebUI debug chat):
				// persist and broadcast directly so WebUI receives the reply cleanly.
				rc.Status = StatusCompleted
				botReplied = true
			}

			if botReplied {
				p.recordBotReplyTimeline(ctx, rc)
			}
		}
	}

	if rc.FinishedAt.IsZero() {
		rc.FinishedAt = p.clock()
	}

	postStep := rc.AppendStep(TraceStep{
		Stage:      StagePostRun,
		Name:       "deliver_and_record",
		Status:     postRunStepStatus(rc.Status),
		DurationMs: time.Since(t0).Milliseconds(),
		Detail: map[string]any{
			"status":  string(rc.Status),
			"actions": len(rc.Actions),
			"silent":  rc.Silent,
		},
	})

	p.persistRunTrace(ctx, rc)

	finalStage := StageDone
	if rc.Status == StatusError {
		finalStage = StageError
	}
	p.broadcastTraceStep(rc, finalStage, &postStep)

	// Non-blocking async background maintenance (never blocks the room's serial lane).
	if !p.disableAsyncBg {
		p.dispatchBackgroundMaintenance(rc, botReplied)
	}
	return nil
}

func (p *Pipeline) enrichOutboundActions(ctx context.Context, actions []protocol.OutboundAction) []protocol.OutboundAction {
	out := make([]protocol.OutboundAction, 0, len(actions))
	var speechModel ai.SpeechModel
	if reg := p.aiGetter(); reg != nil {
		if sm, err := reg.Speech(ai.SlotTTS); err == nil {
			speechModel = sm
		}
	}

	for _, act := range actions {
		switch act.Type {
		case protocol.ActionSendVoice:
			if speechModel != nil && p.blobs != nil && strings.TrimSpace(act.Text) != "" {
				resp, err := speechModel.Synthesize(ctx, ai.SpeechRequest{Text: act.Text})
				if err != nil {
					p.logger.Warn("tts synthesize failed; falling back to text", "err", err)
				} else if len(resp.Audio) > 0 {
					mimeType := resp.MIMEType
					if mimeType == "" {
						mimeType = "audio/mpeg"
					}
					meta, err := p.blobs.PutBlob(ctx, bytes.NewReader(resp.Audio), mimeType)
					if err != nil {
						p.logger.Warn("store tts audio blob failed", "err", err)
					} else {
						act.Media = &protocol.MediaRef{
							BlobID:    meta.BlobID,
							MIMEType:  meta.MIMEType,
							SizeBytes: meta.SizeBytes,
						}
					}
				}
			}

		case protocol.ActionSendMeme:
			if act.MemeRef != "" {
				m, ok, err := p.store.FindMemeByPrefix(ctx, act.MemeRef)
				if err == nil && ok {
					act.MemeRef = m.Hash
					if p.blobs != nil && m.RelPath != "" {
						if rc, err := p.blobs.OpenMemeFile(ctx, m.RelPath); err == nil {
							raw, readErr := io.ReadAll(rc)
							_ = rc.Close()
							if readErr == nil && len(raw) > 0 {
								mimeType := http.DetectContentType(raw)
								if meta, err := p.blobs.PutBlob(ctx, bytes.NewReader(raw), mimeType); err == nil {
									act.Media = &protocol.MediaRef{
										BlobID:    meta.BlobID,
										MIMEType:  meta.MIMEType,
										SizeBytes: meta.SizeBytes,
									}
								}
							}
						}
					}
					if p.memes != nil {
						_ = p.memes.RecordUse(ctx, m.Hash)
					}
				}
			}
		}
		out = append(out, act)
	}
	return out
}

func (p *Pipeline) recordBotReplyTimeline(ctx context.Context, rc *RunContext) {
	nowMs := p.clock().UnixMilli()
	var blocks []protocol.ContentBlock
	var replyTo string
	for _, act := range rc.Actions {
		if replyTo == "" && act.ReplyToMsgID != "" {
			replyTo = act.ReplyToMsgID
		}
		switch act.Type {
		case protocol.ActionSendText:
			blocks = append(blocks, protocol.ContentBlock{
				Type: protocol.BlockText,
				Text: act.Text,
			})
		case protocol.ActionSendVoice:
			blocks = append(blocks, protocol.ContentBlock{
				Type:  protocol.BlockAudio,
				Text:  act.Text,
				Media: act.Media,
			})
		case protocol.ActionSendMeme:
			blocks = append(blocks, protocol.ContentBlock{
				Type:        protocol.BlockImage,
				Description: "[meme:#" + meme.ShortHash(act.MemeRef) + "]",
				Media:       act.Media,
			})
		}
	}
	if len(blocks) == 0 {
		return
	}

	msgID := "bot-" + randomHex(6)
	if len(rc.DeliveredMsgIDs) > 0 && rc.DeliveredMsgIDs[0] != "" {
		msgID = rc.DeliveredMsgIDs[0]
	}

	blocksJSON, _ := json.Marshal(blocks)
	rendered := RenderBlocksText(blocks)

	botName := p.resolveBotName()
	_, err := p.store.AppendTimelineMessage(ctx, store.TimelineMessage{
		ChannelID:    rc.ChannelID,
		RoomID:       rc.RoomID,
		MsgID:        msgID,
		SenderID:     p.botUserID,
		SenderName:   botName,
		IsBot:        true,
		TimestampMs:  nowMs,
		ContentJSON:  string(blocksJSON),
		RenderedText: rendered,
		Status:       "PROCESSED",
	})
	if err != nil {
		p.logger.Warn("failed to persist bot reply to timeline", "room_id", rc.RoomID, "err", err)
	}

	p.broadcastTimelineAppend(protocol.InboundMessage{
		ChannelID:    rc.ChannelID,
		RoomID:       rc.RoomID,
		MsgID:        msgID,
		SenderID:     p.botUserID,
		SenderName:   botName,
		IsBot:        true,
		TimestampMs:  nowMs,
		Blocks:       blocks,
		ReplyToMsgID: replyTo,
	})
}

func (p *Pipeline) persistRunTrace(ctx context.Context, rc *RunContext) {
	gateBytes, _ := json.Marshal(map[string]any{
		"should_reply": rc.Gate.ShouldReply,
		"score":        rc.Gate.Score,
		"confidence":   rc.Gate.Confidence,
		"bypassed":     rc.Gate.Bypassed,
		"reason":       rc.Gate.Reason,
	})
	padBytes, _ := json.Marshal(StoredTracePAD{
		Before:         rc.Gate.PADBefore,
		After:          rc.Gate.PADAfter,
		InnerFeeling:   rc.Gate.InnerFeeling,
		PrototypeID:    rc.Gate.PrototypeID,
		PrototypeLabel: rc.Gate.PrototypeLabel,
	})

	memHits := make([]TraceMemoryHit, 0, len(rc.MemoryTrace))
	for _, h := range rc.MemoryTrace {
		memHits = append(memHits, TraceMemoryHit{
			ID:      h.MemoryID,
			Content: h.Content,
			Score:   h.Score,
			Source:  classifyMemoryHitSource(h),
			TEvent:  h.TEventDate,
		})
	}
	memBytes, _ := json.Marshal(memHits)

	stepsBytes, _ := json.Marshal(StoredTraceSteps{
		Steps:     rc.Steps,
		ToolCalls: rc.ToolCalls,
		Usage:     rc.TotalUsage,
		Summary:   summarizeRun(rc),
	})

	actions := rc.Actions
	if actions == nil {
		actions = []protocol.OutboundAction{}
	}
	actionsBytes, _ := json.Marshal(actions)

	var finishedMs int64
	if !rc.FinishedAt.IsZero() {
		finishedMs = rc.FinishedAt.UnixMilli()
	}

	trace := store.RunTrace{
		RunID:         rc.RunID,
		RoomID:        rc.RoomID,
		TriggerSource: string(rc.Trigger.Source),
		StartedAtMs:   rc.StartedAt.UnixMilli(),
		FinishedAtMs:  finishedMs,
		Status:        string(rc.Status),
		GateJSON:      string(gateBytes),
		PADJSON:       string(padBytes),
		MemoriesJSON:  string(memBytes),
		StepsJSON:     string(stepsBytes),
		ActionsJSON:   string(actionsBytes),
		ErrorText:     rc.Error,
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := p.store.UpsertRunTrace(saveCtx, trace); err != nil {
		p.logger.Warn("failed to persist run trace", "run_id", rc.RunID, "err", err)
	}
}

func (p *Pipeline) dispatchBackgroundMaintenance(rc *RunContext, botReplied bool) {
	if p.memory == nil || !p.featureFlags().MemoryEnabled {
		return
	}

	roomID := rc.RoomID
	var hitIDs []string
	if botReplied && len(rc.MemoryHits) > 0 {
		hitIDs = make([]string, len(rc.MemoryHits))
		for i, h := range rc.MemoryHits {
			hitIDs[i] = h.Memory.ID
		}
	}

	p.bgWG.Add(1)
	go func() {
		defer p.bgWG.Done()
		bgCtx, cancel := context.WithTimeout(p.bgCtx, 45*time.Second)
		defer cancel()

		if len(hitIDs) > 0 {
			if _, err := p.memory.RecordEffectiveRecall(bgCtx, roomID, hitIDs, true, true); err != nil {
				p.logger.Debug("background recall reinforcement failed", "room_id", roomID, "err", err)
			}
		}

		p.maybeDistillRoom(bgCtx, roomID)
	}()
}

func (p *Pipeline) maybeDistillRoom(ctx context.Context, roomID string) {
	unarchived, err := p.store.ListUnarchivedTimeline(ctx, roomID, p.distillBatchSize)
	if err != nil || len(unarchived) < p.distillBatchSize {
		return
	}

	p.distillMu.Lock()
	if p.distillActive[roomID] {
		p.distillMu.Unlock()
		return
	}
	p.distillActive[roomID] = true
	p.distillMu.Unlock()
	defer func() {
		p.distillMu.Lock()
		delete(p.distillActive, roomID)
		p.distillMu.Unlock()
	}()

	var distiller memory.Distiller
	if reg := p.aiGetter(); reg != nil {
		if lm, err := reg.Language(ai.SlotSummary); err == nil {
			distiller = memory.NewLLMDistiller(lm, p.location)
		} else if lm, err := reg.Language(ai.SlotChat); err == nil {
			distiller = memory.NewLLMDistiller(lm, p.location)
		}
	}
	if distiller == nil {
		return
	}
	if _, err := p.memory.DistillBatchWith(ctx, roomID, unarchived, distiller); err != nil {
		p.logger.Warn("background shallow distillation failed", "room_id", roomID, "err", err)
	}
}

func (p *Pipeline) resolveChatModel() (ai.LanguageModel, error) {
	reg := p.aiGetter()
	if reg == nil {
		return nil, errors.New("kernel: ai registry is not configured")
	}
	lm, err := reg.Language(ai.SlotChat)
	if err != nil {
		return nil, fmt.Errorf("kernel: resolve chat model: %w", err)
	}
	return lm, nil
}

func (p *Pipeline) broadcastPADUpdate(roomID string, trans persona.PADTransition) {
	if p.hub == nil {
		return
	}
	intensity := math.Sqrt(trans.NewPAD.P*trans.NewPAD.P + trans.NewPAD.A*trans.NewPAD.A + trans.NewPAD.D*trans.NewPAD.D)
	_ = p.hub.BroadcastEvent(protocol.EventPADUpdate, roomID, map[string]any{
		"value": map[string]float64{
			"p": trans.NewPAD.P,
			"a": trans.NewPAD.A,
			"d": trans.NewPAD.D,
		},
		"feeling":    trans.InnerFeeling,
		"intensity":  intensity,
		"updated_at": trans.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

func (p *Pipeline) broadcastTraceStep(rc *RunContext, stage RunStage, step *TraceStep) {
	if p.hub == nil {
		return
	}
	payload := map[string]any{
		"run_id":  rc.RunID,
		"room_id": rc.RoomID,
		"stage":   string(stage),
	}
	if step != nil {
		payload["step"] = step
	}
	_ = p.hub.BroadcastEvent(protocol.EventTraceStep, rc.RoomID, payload)
}

func (p *Pipeline) broadcastTimelineAppend(msg protocol.InboundMessage) {
	if p.hub == nil {
		return
	}
	blocks := msg.Blocks
	if blocks == nil {
		blocks = []protocol.ContentBlock{}
	}
	_ = p.hub.BroadcastEvent(protocol.EventTimelineAppend, msg.RoomID, map[string]any{
		"msg_id":          msg.MsgID,
		"channel_id":      msg.ChannelID,
		"room_id":         msg.RoomID,
		"sender_id":       msg.SenderID,
		"sender_name":     msg.SenderName,
		"is_bot":          msg.IsBot,
		"timestamp_ms":    msg.TimestampMs,
		"blocks":          blocks,
		"reply_to_msg_id": msg.ReplyToMsgID,
	})
}

// RenderBlocksText converts normalized ContentBlocks into a clean human-readable summary line.
func RenderBlocksText(blocks []protocol.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case protocol.BlockText:
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
			}
		case protocol.BlockMention:
			name := strings.TrimSpace(b.TargetName)
			if name == "" {
				name = strings.TrimSpace(b.TargetID)
			}
			if name != "" {
				parts = append(parts, "@"+name)
			}
		case protocol.BlockLink:
			label := strings.TrimSpace(b.Title)
			if label == "" {
				label = strings.TrimSpace(b.URL)
			}
			if label != "" {
				parts = append(parts, "[链接: "+label+"]")
			}
		case protocol.BlockImage:
			if d := strings.TrimSpace(b.Description); d != "" {
				parts = append(parts, d)
			} else {
				parts = append(parts, "[图片]")
			}
		case protocol.BlockAudio:
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, "[语音: "+t+"]")
			} else {
				parts = append(parts, "[语音]")
			}
		case protocol.BlockVideo:
			if d := strings.TrimSpace(b.Description); d != "" {
				parts = append(parts, "[视频: "+d+"]")
			} else {
				parts = append(parts, "[视频]")
			}
		case protocol.BlockReplyRef:
			if b.ReplyToMsgID != "" {
				parts = append(parts, "[回复:"+b.ReplyToMsgID+"]")
			}
		case protocol.BlockForwardTree:
			parts = append(parts, fmt.Sprintf("[合并转发:%d条]", len(b.ForwardNodes)))
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func formatSpeakerLabel(m store.TimelineMessage, botName string) string {
	if m.IsBot {
		nick := strings.TrimSpace(m.SenderName)
		if nick == "" {
			nick = botName
		}
		return fmt.Sprintf("[我(%s)]", nick)
	}
	if name := strings.TrimSpace(m.SenderName); name != "" {
		return name
	}
	if m.SenderID != "" {
		return m.SenderID
	}
	return "群友"
}

func formatTimelineLines(msgs []store.TimelineMessage, botName string) []string {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		txt := strings.TrimSpace(m.RenderedText)
		if txt == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", formatSpeakerLabel(m, botName), txt))
	}
	return lines
}

func buildQueryText(msgs []store.TimelineMessage) string {
	var parts []string
	for _, m := range msgs {
		if txt := strings.TrimSpace(m.RenderedText); txt != "" {
			parts = append(parts, txt)
		}
	}
	return strings.Join(parts, "\n")
}

func classifyMemoryHitSource(h memory.HitTrace) string {
	for _, cue := range h.MatchedCues {
		if cue == "vector" {
			return "vector"
		}
		if strings.HasPrefix(cue, "entity:") || strings.HasPrefix(cue, "speaker:") {
			return "assoc"
		}
	}
	if h.VectorSim > 0 && h.FTSScore == 0 {
		return "vector"
	}
	if h.EntityBoost > 0 && h.FTSScore == 0 {
		return "assoc"
	}
	return "fts"
}

func summarizeRun(rc *RunContext) string {
	switch rc.Status {
	case StatusGatedOut:
		return fmt.Sprintf("门控跳过 (score=%.2f)", rc.Gate.Score)
	case StatusSilent:
		return "主动保持沉默"
	case StatusError:
		return truncatePreview(rc.Error, 120)
	case StatusCompleted:
		for _, a := range rc.Actions {
			if a.Type == protocol.ActionSendText && strings.TrimSpace(a.Text) != "" {
				return truncatePreview(a.Text, 120)
			}
			if a.Type == protocol.ActionSendVoice && strings.TrimSpace(a.Text) != "" {
				return "[语音] " + truncatePreview(a.Text, 100)
			}
			if a.Type == protocol.ActionSendMeme {
				return "[表情 #" + meme.ShortHash(a.MemeRef) + "]"
			}
		}
		return fmt.Sprintf("投递 %d 个动作", len(rc.Actions))
	default:
		return ""
	}
}

func statusFromErr(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

func postRunStepStatus(s RunStatus) string {
	if s == StatusError {
		return "error"
	}
	if s == StatusGatedOut || s == StatusSilent {
		return "skipped"
	}
	return "ok"
}

func draftToolDefs(spec *RunSpec) []ai.ToolDef {
	if spec == nil {
		return nil
	}
	return spec.ToolDefs
}

func (p *Pipeline) resolveBotName() string {
	if p.behaviorConfig != nil {
		if name := strings.TrimSpace(p.behaviorConfig().BotName); name != "" {
			return name
		}
	}
	return p.botName
}

func (p *Pipeline) resolveGateThresholds() (float64, float64) {
	if p.behaviorConfig != nil {
		beh := p.behaviorConfig()
		th := beh.GateThreshold
		at := beh.GateAtThreshold
		if th <= 0 {
			th = p.gateThreshold
		}
		if at <= 0 {
			at = p.gateAtThreshold
		}
		return th, at
	}
	return p.gateThreshold, p.gateAtThreshold
}

func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
