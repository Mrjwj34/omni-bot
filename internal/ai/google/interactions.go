package google

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
	"google.golang.org/genai/interactions/retry"

	"omni-bot/internal/ai"
)

// minSafeMaxOutputTokens prevents thought tokens from starving model_output
// when a caller specifies a small output cap (verified live on Gemini Interactions API).
const minSafeMaxOutputTokens = 2048

// LanguageModel implements ai.LanguageModel via stateless Gemini Interactions API (store: false).
type LanguageModel struct {
	client   *genai.Client
	provider string
	modelID  string
	caps     ai.Capabilities
	defaults map[string]any
	files    *fileManager
}

func NewLanguageModel(ctx context.Context, resolved ai.ResolvedModel, opts ...Option) (*LanguageModel, error) {
	if strings.TrimSpace(resolved.Model.ModelID) == "" {
		return nil, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: resolved.Provider.ID,
			Message:  "google language model requires ModelID",
		}
	}
	o := resolveOptions(opts)
	client, err := buildGenAIClient(ctx, resolved, o)
	if err != nil {
		return nil, err
	}
	return &LanguageModel{
		client:   client,
		provider: resolved.Provider.ID,
		modelID:  resolved.Model.ModelID,
		caps:     resolved.Model.Capabilities,
		defaults: resolved.Model.DefaultParams,
		files:    newFileManager(client, resolved.Provider.ID, o),
	}, nil
}

func (m *LanguageModel) Capabilities() ai.Capabilities {
	return m.caps
}

func (m *LanguageModel) Generate(ctx context.Context, req ai.GenerateRequest) (ai.GenerateResponse, error) {
	steps, err := m.buildSteps(ctx, req.Messages)
	if err != nil {
		return ai.GenerateResponse{}, err
	}
	if len(steps) == 0 {
		return ai.GenerateResponse{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: m.provider,
			Message:  "at least one input message is required",
		}
	}

	genCfg, err := m.buildGenerationConfig(req)
	if err != nil {
		return ai.GenerateResponse{}, err
	}

	store := false
	in := interactions.NewInteractionsInput(steps)
	body := interactions.CreateModelInteraction{
		Model:            interactions.Model(m.modelID),
		Input:            &in,
		Store:            &store,
		GenerationConfig: genCfg,
	}
	if req.System != "" {
		sys := req.System
		body.SystemInstruction = &sys
	}
	if len(req.Tools) > 0 {
		body.Tools = buildTools(req.Tools)
	}
	if len(req.ResponseSchema) > 0 {
		rf := interactions.NewResponseFormat(req.ResponseSchema)
		cmrf := interactions.NewCreateModelInteractionResponseFormat(rf)
		body.ResponseFormat = &cmrf
	}

	resp, err := m.client.Interactions.Create(
		ctx,
		operations.CreateInteractionRequest{
			Body: operations.NewCreateInteractionRequestBody(body),
		},
		operations.WithRetries(retry.Config{Strategy: "none"}),
		operations.WithAutomaticContinuation(false),
	)
	if err != nil {
		return ai.GenerateResponse{}, classifyError(m.provider, err)
	}
	if resp == nil || resp.Interaction == nil {
		return ai.GenerateResponse{}, &ai.Error{
			Kind:     ai.ErrTransient,
			Provider: m.provider,
			Message:  "empty interaction response",
		}
	}
	return parseInteractionResponse(m.provider, resp.Interaction)
}

func (m *LanguageModel) buildSteps(ctx context.Context, messages []ai.Message) ([]interactions.Step, error) {
	var steps []interactions.Step
	for i, msg := range messages {
		if msg.Role == ai.RoleAssistant && len(msg.NativeContent) > 0 {
			var nativeSteps []interactions.Step
			if err := json.Unmarshal(msg.NativeContent, &nativeSteps); err != nil {
				return nil, &ai.Error{
					Kind:     ai.ErrInvalidArg,
					Provider: m.provider,
					Message:  fmt.Sprintf("decode assistant NativeContent at message %d: %v", i, err),
					Err:      err,
				}
			}
			steps = append(steps, nativeSteps...)
			continue
		}

		switch msg.Role {
		case ai.RoleUser:
			content, err := m.buildContentList(ctx, msg.Parts)
			if err != nil {
				return nil, err
			}
			if len(content) > 0 {
				steps = append(steps, interactions.NewStep(interactions.UserInputStep{Content: content}))
			}

		case ai.RoleAssistant:
			var content []interactions.Content
			var calls []interactions.FunctionCallStep
			for _, p := range msg.Parts {
				if p.Kind == ai.PartToolCall && p.ToolCall != nil {
					args := p.ToolCall.Arguments
					if args == nil {
						args = map[string]any{}
					}
					calls = append(calls, interactions.FunctionCallStep{
						ID:        p.ToolCall.ID,
						Name:      p.ToolCall.Name,
						Arguments: args,
					})
					continue
				}
				c, err := m.buildSingleContent(ctx, p)
				if err != nil {
					return nil, err
				}
				content = append(content, c)
			}
			if len(content) > 0 {
				steps = append(steps, interactions.NewStep(interactions.ModelOutputStep{Content: content}))
			}
			for _, call := range calls {
				steps = append(steps, interactions.NewStep(call))
			}

		case ai.RoleTool:
			for _, p := range msg.Parts {
				if p.Kind != ai.PartToolResult || p.ToolResult == nil {
					continue
				}
				fr, err := buildFunctionResultStep(m.provider, p.ToolResult)
				if err != nil {
					return nil, err
				}
				steps = append(steps, interactions.NewStep(fr))
			}

		default:
			return nil, &ai.Error{
				Kind:     ai.ErrInvalidArg,
				Provider: m.provider,
				Message:  fmt.Sprintf("unsupported message role %q", msg.Role),
			}
		}
	}
	return steps, nil
}

func (m *LanguageModel) buildContentList(ctx context.Context, parts []ai.Part) ([]interactions.Content, error) {
	out := make([]interactions.Content, 0, len(parts))
	for _, p := range parts {
		c, err := m.buildSingleContent(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (m *LanguageModel) buildSingleContent(ctx context.Context, p ai.Part) (interactions.Content, error) {
	switch p.Kind {
	case ai.PartText:
		return interactions.NewContent(interactions.TextContent{Text: p.Text}), nil
	case ai.PartImage, ai.PartAudio, ai.PartVideo:
		return m.files.toInteractionContent(ctx, p.Kind, p.Media)
	default:
		return interactions.Content{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: m.provider,
			Message:  fmt.Sprintf("unsupported content part kind %q", p.Kind),
		}
	}
}

func buildFunctionResultStep(provider string, tr *ai.ToolResult) (interactions.FunctionResultStep, error) {
	// Gemini Interactions API treats JSON arrays in FunctionResultStep.Result as typed []Content
	// (requiring a "type" field per element); sending a plain string avoids schema rejection for
	// slice-shaped structured tool outputs while preserving full readable content.
	resultText := tr.Content
	if resultText == "" && tr.Data != nil {
		raw, err := json.Marshal(tr.Data)
		if err != nil {
			return interactions.FunctionResultStep{}, &ai.Error{
				Kind:     ai.ErrInvalidArg,
				Provider: provider,
				Message:  fmt.Sprintf("marshal tool result %q: %v", tr.Name, err),
				Err:      err,
			}
		}
		resultText = string(raw)
	}
	union := interactions.NewFunctionResultStepResultUnion(resultText)

	step := interactions.FunctionResultStep{
		CallID: tr.CallID,
		Result: union,
	}
	if tr.Name != "" {
		name := tr.Name
		step.Name = &name
	}
	if tr.IsError {
		isErr := true
		step.IsError = &isErr
	}
	return step, nil
}

func buildTools(defs []ai.ToolDef) []interactions.Tool {
	tools := make([]interactions.Tool, 0, len(defs))
	for _, d := range defs {
		name := d.Name
		fn := interactions.Function{
			Name:       &name,
			Parameters: d.Parameters,
		}
		if d.Description != "" {
			desc := d.Description
			fn.Description = &desc
		}
		tools = append(tools, interactions.NewTool(fn))
	}
	return tools
}

func (m *LanguageModel) buildGenerationConfig(req ai.GenerateRequest) (*interactions.GenerationConfig, error) {
	level := req.ThinkingLevel
	if level == "" && m.defaults != nil {
		if v, ok := m.defaults["thinking_level"].(string); ok {
			level = v
		}
	}
	thinking, err := NormalizeThinkingLevel(m.provider, m.modelID, level)
	if err != nil {
		return nil, err
	}

	maxOut := req.MaxOutputTokens
	if maxOut == 0 && m.defaults != nil {
		switch v := m.defaults["max_output_tokens"].(type) {
		case int:
			maxOut = v
		case float64:
			maxOut = int(v)
		}
	}

	var cfg interactions.GenerationConfig
	hasAny := false

	if thinking != nil {
		cfg.ThinkingLevel = thinking
		hasAny = true
	}
	if maxOut > 0 {
		if maxOut < minSafeMaxOutputTokens {
			maxOut = minSafeMaxOutputTokens
		}
		cfg.MaxOutputTokens = &maxOut
		hasAny = true
	}
	if len(req.StopSequences) > 0 {
		cfg.StopSequences = req.StopSequences
		hasAny = true
	}
	if req.Seed != nil {
		seed := *req.Seed
		cfg.Seed = &seed
		hasAny = true
	}
	if !hasAny {
		return nil, nil
	}
	return &cfg, nil
}

// NormalizeThinkingLevel validates and maps thinking_level to values accepted by the target
// Gemini model (live-verified: minimal is rejected on all models; xhigh is rejected on flash).
func NormalizeThinkingLevel(provider, modelID, level string) (*interactions.ThinkingLevel, error) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	if normalized == "" {
		return nil, nil
	}
	isFlash := strings.Contains(strings.ToLower(modelID), "flash")

	switch normalized {
	case "minimal":
		return interactions.ThinkingLevelLow.ToPointer(), nil
	case "low":
		return interactions.ThinkingLevelLow.ToPointer(), nil
	case "medium":
		return interactions.ThinkingLevelMedium.ToPointer(), nil
	case "high":
		return interactions.ThinkingLevelHigh.ToPointer(), nil
	case "xhigh":
		if isFlash {
			return interactions.ThinkingLevelHigh.ToPointer(), nil
		}
		tl := interactions.ThinkingLevel("xhigh")
		return &tl, nil
	default:
		return nil, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: provider,
			Message:  fmt.Sprintf("unsupported thinking_level %q", level),
		}
	}
}

func parseInteractionResponse(provider string, inter *interactions.Interaction) (ai.GenerateResponse, error) {
	var (
		textParts []string
		parts     []ai.Part
		toolCalls []ai.ToolCall
	)

	for _, step := range inter.Steps {
		if step.ModelOutputStep != nil {
			for _, c := range step.ModelOutputStep.Content {
				if c.TextContent != nil && c.TextContent.Text != "" {
					textParts = append(textParts, c.TextContent.Text)
					parts = append(parts, ai.TextPart(c.TextContent.Text))
				}
			}
		}
		if step.FunctionCallStep != nil {
			fc := ai.ToolCall{
				ID:        step.FunctionCallStep.ID,
				Name:      step.FunctionCallStep.Name,
				Arguments: step.FunctionCallStep.Arguments,
			}
			toolCalls = append(toolCalls, fc)
			parts = append(parts, ai.ToolCallPart(fc))
		}
	}

	var native json.RawMessage
	if len(inter.Steps) > 0 {
		raw, err := json.Marshal(inter.Steps)
		if err != nil {
			return ai.GenerateResponse{}, &ai.Error{
				Kind:     ai.ErrUnknown,
				Provider: provider,
				Message:  fmt.Sprintf("marshal native steps: %v", err),
				Err:      err,
			}
		}
		native = raw
	}

	return ai.GenerateResponse{
		Text:          strings.Join(textParts, "\n"),
		Parts:         parts,
		ToolCalls:     toolCalls,
		Status:        string(inter.Status),
		Usage:         mapUsage(inter.Usage),
		NativeContent: native,
	}, nil
}

func mapUsage(u *interactions.Usage) ai.Usage {
	if u == nil {
		return ai.Usage{}
	}
	return ai.Usage{
		InputTokens:   cloneIntPtr(u.TotalInputTokens),
		OutputTokens:  cloneIntPtr(u.TotalOutputTokens),
		CachedTokens:  cloneIntPtr(u.TotalCachedTokens),
		ThoughtTokens: cloneIntPtr(u.TotalThoughtTokens),
		ToolUseTokens: cloneIntPtr(u.TotalToolUseTokens),
		TotalTokens:   cloneIntPtr(u.TotalTokens),
	}
}

func cloneIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
