package ai

import (
	"context"
	"encoding/json"
	"time"
)

type Slot string

const (
	SlotChat      Slot = "chat"
	SlotDecision  Slot = "decision"
	SlotSummary   Slot = "summary"
	SlotVision    Slot = "vision"
	SlotEmbedding Slot = "embedding"
	SlotTTS       Slot = "tts"
	SlotASR       Slot = "asr"
)

// ProviderConfig holds connection and credential settings for an AI provider.
type ProviderConfig struct {
	ID       string
	Kind     string
	BaseURL  string
	APIKeys  []string
	Headers  map[string]string
	ProxyURL string
	Timeout  time.Duration
}

// Capabilities declares which multimodal and agentic features a LanguageModel natively supports.
type Capabilities struct {
	Vision   bool
	AudioIn  bool
	VideoIn  bool
	ToolUse  bool
	Thinking bool
}

// ModelConfig binds a logical model ID to a provider, protocol adapter, and capabilities.
type ModelConfig struct {
	ID            string
	ProviderID    string
	Protocol      string
	ModelID       string
	Capabilities  Capabilities
	DefaultParams map[string]any
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type PartKind string

const (
	PartText       PartKind = "text"
	PartImage      PartKind = "image"
	PartAudio      PartKind = "audio"
	PartVideo      PartKind = "video"
	PartToolCall   PartKind = "tool_call"
	PartToolResult PartKind = "tool_result"
)

type MediaResolution string

const (
	ResolutionAuto   MediaResolution = ""
	ResolutionLow    MediaResolution = "low"
	ResolutionMedium MediaResolution = "medium"
	ResolutionHigh   MediaResolution = "high"
)

// MediaRef identifies a media attachment either by BlobID, local file Path, or inline Data bytes.
type MediaRef struct {
	BlobID     string
	MIMEType   string
	SizeBytes  int64
	DurationMs int64
	Path       string
	Data       []byte
	Caption    string
	Resolution MediaResolution
	Meta       map[string]string
}

// BlobReader loads raw bytes for a MediaRef when only BlobID is supplied.
type BlobReader interface {
	ReadBlob(ctx context.Context, blobID string) ([]byte, error)
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

type ToolResult struct {
	CallID  string
	Name    string
	Content string
	Data    any
	IsError bool
}

type Part struct {
	Kind       PartKind
	Text       string
	Media      *MediaRef
	ToolCall   *ToolCall
	ToolResult *ToolResult
}

func TextPart(text string) Part {
	return Part{Kind: PartText, Text: text}
}

func MediaPart(kind PartKind, ref MediaRef) Part {
	r := ref
	return Part{Kind: kind, Media: &r}
}

func ToolCallPart(call ToolCall) Part {
	c := call
	return Part{Kind: PartToolCall, ToolCall: &c}
}

func ToolResultPart(res ToolResult) Part {
	r := res
	return Part{Kind: PartToolResult, ToolResult: &r}
}

// Message represents one conversational turn. NativeContent carries opaque
// protocol-native steps (e.g. Gemini thought+signature and function_call steps)
// for lossless stateless continuation within a Run.
type Message struct {
	Role          Role
	Parts         []Part
	NativeContent json.RawMessage
}

type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Usage distinguishes absent metrics (nil) from explicit zero values (*0).
type Usage struct {
	InputTokens   *int
	OutputTokens  *int
	CachedTokens  *int
	ThoughtTokens *int
	ToolUseTokens *int
	TotalTokens   *int
}

func IntPtr(v int) *int {
	return &v
}

type GenerateRequest struct {
	System          string
	Messages        []Message
	Tools           []ToolDef
	ResponseSchema  map[string]any
	ThinkingLevel   string
	MaxOutputTokens int
	StopSequences   []string
	Seed            *int
	Params          map[string]any
}

type GenerateResponse struct {
	Text          string
	Parts         []Part
	ToolCalls     []ToolCall
	Status        string
	Usage         Usage
	NativeContent json.RawMessage
}

type DecisionKind string

const (
	DecisionNoul   DecisionKind = "noul"
	DecisionScore  DecisionKind = "score"
	DecisionChoice DecisionKind = "choice"
)

type DecisionQuestion struct {
	Kind             DecisionKind
	Instructions     string
	TrueDescription  string
	FalseDescription string
	Criteria         []string
	Choices          map[string]string
}

type DecisionRequest struct {
	Input     string
	Parts     []Part
	Questions map[string]DecisionQuestion
}

type DecisionAnswer struct {
	Kind            DecisionKind
	Probability     float64
	Score           float64
	NormalizedScore float64
	Choice          string
	Confidence      float64
	Probabilities   map[string]float64
	Legend          map[string]string
}

type DecisionResult struct {
	Answers map[string]DecisionAnswer
	Usage   Usage
}

type EmbedRequest struct {
	Texts      []string
	Dimensions int
}

type EmbedResponse struct {
	Vectors [][]float32
	Usage   Usage
}

type SpeechRequest struct {
	Text    string
	Format  string
	VoiceID string
	Model   string
}

type SpeechResponse struct {
	Audio    []byte
	MIMEType string
}

type TranscribeRequest struct {
	Audio    []byte
	Path     string
	MIMEType string
	Language string
	BlobID   string
}

type TranscribeResponse struct {
	Text       string
	Language   string
	DurationMs int64
}

type LanguageModel interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
	Capabilities() Capabilities
}

type DecisionModel interface {
	Decide(ctx context.Context, req DecisionRequest) (DecisionResult, error)
}

type EmbeddingModel interface {
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
}

type SpeechModel interface {
	Synthesize(ctx context.Context, req SpeechRequest) (SpeechResponse, error)
}

type TranscriptionModel interface {
	Transcribe(ctx context.Context, req TranscribeRequest) (TranscribeResponse, error)
}
