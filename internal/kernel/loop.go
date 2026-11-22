package kernel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"omni-bot/internal/ai"
)

const (
	defaultRetryBackoff = 250 * time.Millisecond
	maxToolPreviewRunes = 240
)

var (
	ErrNilRunSpec       = errors.New("kernel: run spec is nil")
	ErrNilLanguageModel = errors.New("kernel: language model is nil")
	ErrMaxStepsReached  = errors.New("kernel: agent loop reached maximum steps without final response")
)

// LoopOptions configures AgentLoop execution and observability callbacks.
type LoopOptions struct {
	MaxSteps     int
	RetryBackoff time.Duration
	OnStep       func(rc *RunContext, step TraceStep)
}

// AgentLoop executes up to MaxAgentSteps (<= 6) model/tool rounds against a frozen RunSpec.
type AgentLoop struct {
	maxSteps     int
	retryBackoff time.Duration
	onStep       func(rc *RunContext, step TraceStep)
}

// NewAgentLoop constructs a bounded AgentLoop. MaxSteps is hard-capped at MaxAgentSteps (6).
func NewAgentLoop(opts LoopOptions) *AgentLoop {
	maxSteps := opts.MaxSteps
	if maxSteps <= 0 || maxSteps > MaxAgentSteps {
		maxSteps = MaxAgentSteps
	}
	backoff := opts.RetryBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}
	return &AgentLoop{
		maxSteps:     maxSteps,
		retryBackoff: backoff,
		onStep:       opts.OnStep,
	}
}

// Execute runs the bounded agent loop using the frozen spec, keeping SystemInstruction and ToolDefs
// byte-identical across steps and appending opaque NativeContent + tool results for prefix-cache hits.
func (l *AgentLoop) Execute(ctx context.Context, rc *RunContext, spec *RunSpec, model ai.LanguageModel) error {
	if spec == nil {
		return ErrNilRunSpec
	}
	if model == nil {
		return ErrNilLanguageModel
	}
	if rc == nil {
		rc = &RunContext{}
	}

	limit := l.maxSteps
	if spec.MaxSteps > 0 && spec.MaxSteps < limit {
		limit = spec.MaxSteps
	}
	if limit > MaxAgentSteps {
		limit = MaxAgentSteps
	}

	history := slices.Clone(spec.InitialMessages)

	for round := range limit {
		if err := ctx.Err(); err != nil {
			return err
		}

		req := spec.BaseGenerateRequest(history)
		t0 := time.Now()
		resp, retried, err := l.generateWithControlledRetry(ctx, model, req)
		elapsedMs := time.Since(t0).Milliseconds()

		if err != nil {
			step := rc.AppendStep(TraceStep{
				Stage:      StageLoop,
				Name:       fmt.Sprintf("llm_round_%d", round+1),
				Status:     "error",
				DurationMs: elapsedMs,
				Detail: map[string]any{
					"round":   round + 1,
					"retried": retried,
					"error":   err.Error(),
				},
			})
			l.emitStep(rc, step)
			return fmt.Errorf("kernel: agent loop round %d: %w", round+1, err)
		}

		rc.TotalUsage.Add(resp.Usage)
		step := rc.AppendStep(TraceStep{
			Stage:        StageLoop,
			Name:         fmt.Sprintf("llm_round_%d", round+1),
			Status:       "ok",
			DurationMs:   elapsedMs,
			TokensIn:     derefInt(resp.Usage.InputTokens),
			TokensOut:    derefInt(resp.Usage.OutputTokens),
			CachedTokens: derefInt(resp.Usage.CachedTokens),
			Detail: map[string]any{
				"round":      round + 1,
				"tool_calls": len(resp.ToolCalls),
				"retried":    retried,
			},
		})
		l.emitStep(rc, step)

		if len(resp.ToolCalls) == 0 {
			rc.RawOutput = resp.Text
			return nil
		}

		assistantMsg := buildAssistantToolMessage(resp)
		history = append(history, assistantMsg)

		toolResultParts := make([]ai.Part, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			if err := ctx.Err(); err != nil {
				return err
			}

			callT0 := time.Now()
			var resText string
			var resData any
			var isErr bool

			if spec.ToolSnapshot == nil {
				resText = fmt.Sprintf("error: tool %q unavailable (no tool registry configured)", call.Name)
				isErr = true
			} else {
				execRes := spec.ToolSnapshot.ExecuteMap(ctx, call.Name, call.Arguments)
				resText = execRes.Text
				resData = execRes.Structured
				isErr = execRes.IsError
			}
			callDurMs := time.Since(callT0).Milliseconds()

			tc := TraceToolCall{
				Name:          call.Name,
				Args:          call.Arguments,
				ResultPreview: truncatePreview(resText, maxToolPreviewRunes),
				DurationMs:    callDurMs,
				IsError:       isErr,
			}
			rc.ToolCalls = append(rc.ToolCalls, tc)

			status := "ok"
			if isErr {
				status = "error"
			}
			tStep := rc.AppendStep(TraceStep{
				Stage:      StageLoop,
				Name:       "tool:" + call.Name,
				Status:     status,
				DurationMs: callDurMs,
				Detail: map[string]any{
					"tool":     call.Name,
					"is_error": isErr,
					"preview":  tc.ResultPreview,
				},
			})
			l.emitStep(rc, tStep)

			toolResultParts = append(toolResultParts, ai.ToolResultPart(ai.ToolResult{
				CallID:  call.ID,
				Name:    call.Name,
				Content: resText,
				Data:    resData,
				IsError: isErr,
			}))
		}

		history = append(history, ai.Message{
			Role:  ai.RoleTool,
			Parts: toolResultParts,
		})
	}

	return ErrMaxStepsReached
}

// generateWithControlledRetry retries at most ONCE for retryable transient/rate-limit errors.
// Auth, invalid-argument, context-too-long, and canceled errors fail immediately.
func (l *AgentLoop) generateWithControlledRetry(
	ctx context.Context,
	model ai.LanguageModel,
	req ai.GenerateRequest,
) (ai.GenerateResponse, bool, error) {
	resp, err := model.Generate(ctx, req)
	if err == nil {
		return resp, false, nil
	}
	if !ai.IsRetryable(err) {
		return ai.GenerateResponse{}, false, err
	}

	wait := l.retryBackoff
	var aiErr *ai.Error
	if errors.As(err, &aiErr) && aiErr.RetryAfter > 0 {
		wait = min(aiErr.RetryAfter, 5*time.Second)
	}

	timer := time.NewTimer(wait)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ai.GenerateResponse{}, false, ctx.Err()
	case <-timer.C:
	}

	resp, err = model.Generate(ctx, req)
	return resp, true, err
}

func (l *AgentLoop) emitStep(rc *RunContext, step TraceStep) {
	if l.onStep != nil {
		l.onStep(rc, step)
	}
}

func buildAssistantToolMessage(resp ai.GenerateResponse) ai.Message {
	parts := slices.Clone(resp.Parts)
	if len(parts) == 0 {
		if strings.TrimSpace(resp.Text) != "" {
			parts = append(parts, ai.TextPart(resp.Text))
		}
		for _, tc := range resp.ToolCalls {
			parts = append(parts, ai.ToolCallPart(tc))
		}
	}
	return ai.Message{
		Role:          ai.RoleAssistant,
		Parts:         parts,
		NativeContent: slices.Clone(resp.NativeContent),
	}
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func truncatePreview(s string, maxRunes int) string {
	trimmed := strings.TrimSpace(s)
	runes := []rune(trimmed)
	if len(runes) <= maxRunes {
		return trimmed
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "…"
}
