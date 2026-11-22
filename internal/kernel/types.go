package kernel

import (
	"slices"
	"strings"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/meme"
	"omni-bot/internal/memory"
	"omni-bot/internal/persona/pad"
	"omni-bot/internal/protocol"
	"omni-bot/internal/store"
	"omni-bot/internal/tools"
)

// TriggerSource classifies what initiated a Kernel Run.
type TriggerSource string

const (
	TriggerMessage   TriggerSource = "message"
	TriggerScheduled TriggerSource = "scheduled"
	TriggerWebhook   TriggerSource = "webhook"
	TriggerWebUI     TriggerSource = "webui"
)

// TriggerEvent is the unified envelope submitted to SingleFlightBus from any external source.
type TriggerEvent struct {
	Source     TriggerSource `json:"source"`
	ChannelID  string        `json:"channel_id,omitempty"`
	RoomID     string        `json:"room_id"`
	BypassGate bool          `json:"bypass_gate,omitempty"`
	Payload    any           `json:"payload,omitempty"`
	ReceivedAt time.Time     `json:"received_at"`
}

// RunStage matches the WebUI RunStage union in web/src/lib/api/types.ts.
type RunStage string

const (
	StageIdle     RunStage = "idle"
	StageGate     RunStage = "gate"
	StageAssemble RunStage = "assemble"
	StageLoop     RunStage = "loop"
	StagePostRun  RunStage = "post_run"
	StageDone     RunStage = "done"
	StageError    RunStage = "error"
)

// RunStatus matches the WebUI RunTraceSummary.status union in web/src/lib/api/types.ts.
type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusSilent    RunStatus = "silent"
	StatusGatedOut  RunStatus = "gated_out"
	StatusError     RunStatus = "error"
)

// TraceStep mirrors web/src/lib/api/types.ts TraceStep 1:1.
type TraceStep struct {
	StepIndex    int            `json:"step_index"`
	Stage        RunStage       `json:"stage"`
	Name         string         `json:"name"`
	Status       string         `json:"status"`
	DurationMs   int64          `json:"duration_ms,omitempty"`
	TokensIn     int            `json:"tokens_in,omitempty"`
	TokensOut    int            `json:"tokens_out,omitempty"`
	CachedTokens int            `json:"cached_tokens,omitempty"`
	Detail       map[string]any `json:"detail,omitempty"`
	TimestampMs  int64          `json:"timestamp_ms"`
}

// TraceMemoryHit mirrors web/src/lib/api/types.ts TraceMemoryHit 1:1.
type TraceMemoryHit struct {
	ID      string  `json:"id"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
	Source  string  `json:"source"`
	TEvent  string  `json:"t_event,omitempty"`
}

// TraceToolCall mirrors web/src/lib/api/types.ts TraceToolCall 1:1.
type TraceToolCall struct {
	Name          string         `json:"name"`
	Server        string         `json:"server,omitempty"`
	Args          map[string]any `json:"args,omitempty"`
	ResultPreview string         `json:"result_preview,omitempty"`
	DurationMs    int64          `json:"duration_ms,omitempty"`
	IsError       bool           `json:"is_error,omitempty"`
}

// UsageSummary aggregates token usage across all LLM steps within a Run.
type UsageSummary struct {
	InputTokens   int `json:"input_tokens,omitempty"`
	OutputTokens  int `json:"output_tokens,omitempty"`
	CachedTokens  int `json:"cached_tokens,omitempty"`
	ThoughtTokens int `json:"thought_tokens,omitempty"`
	ToolUseTokens int `json:"tool_use_tokens,omitempty"`
}

func (u *UsageSummary) Add(usage ai.Usage) {
	if usage.InputTokens != nil {
		u.InputTokens += *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		u.OutputTokens += *usage.OutputTokens
	}
	if usage.CachedTokens != nil {
		u.CachedTokens += *usage.CachedTokens
	}
	if usage.ThoughtTokens != nil {
		u.ThoughtTokens += *usage.ThoughtTokens
	}
	if usage.ToolUseTokens != nil {
		u.ToolUseTokens += *usage.ToolUseTokens
	}
}

// GateDecision captures System 1 gate evaluation, PAD transition, and precomputed query embedding.
type GateDecision struct {
	ShouldReply    bool           `json:"should_reply"`
	Score          float64        `json:"score"`
	Confidence     float64        `json:"confidence,omitempty"`
	Bypassed       bool           `json:"bypassed,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	QueryText      string         `json:"query_text,omitempty"`
	QueryVector    []float32      `json:"-"`
	PADBefore      *pad.Vec3      `json:"pad_before,omitempty"`
	PADAfter       *pad.Vec3      `json:"pad_after,omitempty"`
	InnerFeeling   string         `json:"inner_feeling,omitempty"`
	PrototypeID    string         `json:"prototype_id,omitempty"`
	PrototypeLabel string         `json:"prototype_label,omitempty"`
	Usage          ai.Usage       `json:"-"`
	Extra          map[string]any `json:"extra,omitempty"`
}

// RunSpec is the immutable execution specification frozen at the end of OnAssemble.
// Keeping SystemInstruction and ToolDefs byte-identical across Agent Loop steps ensures
// stable intra-run prefix cache hits on Gemini Interactions API.
type RunSpec struct {
	SystemInstruction string
	InitialMessages   []ai.Message
	ToolDefs          []ai.ToolDef
	ToolSnapshot      *tools.Registry
	MaxSteps          int
	ThinkingLevel     string
	MaxOutputTokens   int
}

// BaseGenerateRequest constructs a GenerateRequest using the frozen system prompt and tools
// paired with the provided step history.
func (s *RunSpec) BaseGenerateRequest(messages []ai.Message) ai.GenerateRequest {
	return ai.GenerateRequest{
		System:          s.SystemInstruction,
		Messages:        messages,
		Tools:           s.ToolDefs,
		ThinkingLevel:   s.ThinkingLevel,
		MaxOutputTokens: s.MaxOutputTokens,
	}
}

// ContextDraft accumulates static and dynamic prompt fragments during OnAssemble before freezing.
type ContextDraft struct {
	StaticBlocks    []string
	DynamicBlocks   []string
	MediaParts      []ai.Part
	ExtraMessages   []ai.Message
	Tools           *tools.Registry
	MaxSteps        int
	ThinkingLevel   string
	MaxOutputTokens int
}

// AddStatic appends a non-empty fragment to the low-frequency system instruction prefix.
func (d *ContextDraft) AddStatic(block string) {
	if trimmed := strings.TrimSpace(block); trimmed != "" {
		d.StaticBlocks = append(d.StaticBlocks, trimmed)
	}
}

// AddDynamic appends a non-empty fragment to the dynamic per-run user context block.
func (d *ContextDraft) AddDynamic(block string) {
	if trimmed := strings.TrimSpace(block); trimmed != "" {
		d.DynamicBlocks = append(d.DynamicBlocks, trimmed)
	}
}

// Freeze converts the mutable draft into an immutable RunSpec with deterministic sorted tool schemas.
func (d *ContextDraft) Freeze() *RunSpec {
	sys := strings.Join(d.StaticBlocks, "\n\n")

	var toolSnap *tools.Registry
	var toolDefs []ai.ToolDef
	if d.Tools != nil {
		toolSnap = d.Tools.Snapshot()
		toolDefs = toolSnap.ToolDefs()
	}

	var initialMsgs []ai.Message
	if len(d.DynamicBlocks) > 0 || len(d.MediaParts) > 0 {
		parts := make([]ai.Part, 0, 1+len(d.MediaParts))
		if len(d.DynamicBlocks) > 0 {
			parts = append(parts, ai.TextPart(strings.Join(d.DynamicBlocks, "\n\n")))
		}
		parts = append(parts, d.MediaParts...)
		initialMsgs = append(initialMsgs, ai.Message{
			Role:  ai.RoleUser,
			Parts: parts,
		})
	}
	if len(d.ExtraMessages) > 0 {
		initialMsgs = append(initialMsgs, slices.Clone(d.ExtraMessages)...)
	}

	maxSteps := d.MaxSteps
	if maxSteps <= 0 || maxSteps > MaxAgentSteps {
		maxSteps = MaxAgentSteps
	}

	return &RunSpec{
		SystemInstruction: sys,
		InitialMessages:   initialMsgs,
		ToolDefs:          toolDefs,
		ToolSnapshot:      toolSnap,
		MaxSteps:          maxSteps,
		ThinkingLevel:     d.ThinkingLevel,
		MaxOutputTokens:   d.MaxOutputTokens,
	}
}

// MaxAgentSteps is the hard upper bound on model/tool rounds within a single Run.
const MaxAgentSteps = 6

// RunContext carries per-run state through OnGate -> OnAssemble -> Agent Loop -> OnPostRun.
type RunContext struct {
	RunID           string
	RoomID          string
	ChannelID       string
	Trigger         TriggerEvent
	StartedAt       time.Time
	FinishedAt      time.Time
	PendingMessages []store.TimelineMessage
	RecentMessages  []store.TimelineMessage
	Gate            GateDecision
	MemoryHits      []memory.RecallHit
	MemoryTrace     []memory.HitTrace
	HandMemes       []meme.HandItem
	Spec            *RunSpec
	Steps           []TraceStep
	ToolCalls       []TraceToolCall
	RawOutput       string
	Actions         []protocol.OutboundAction
	DeliveredMsgIDs []string
	Silent          bool
	TotalUsage      UsageSummary
	Status          RunStatus
	Error           string
	ReleaseTools    func()
}

// AppendStep appends a structured TraceStep and assigns its sequential StepIndex.
func (rc *RunContext) AppendStep(step TraceStep) TraceStep {
	step.StepIndex = len(rc.Steps)
	if step.TimestampMs == 0 {
		step.TimestampMs = time.Now().UnixMilli()
	}
	rc.Steps = append(rc.Steps, step)
	return step
}
