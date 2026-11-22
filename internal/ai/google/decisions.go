package google

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/decisions"
	"google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
	"google.golang.org/genai/interactions/retry"

	"omni-bot/internal/ai"
)

// DecisionModel implements ai.DecisionModel via Gemini Decisions API (/v1beta/decisions).
type DecisionModel struct {
	client   *genai.Client
	provider string
	modelID  string
	files    *fileManager
}

func NewDecisionModel(ctx context.Context, resolved ai.ResolvedModel, opts ...Option) (*DecisionModel, error) {
	if strings.TrimSpace(resolved.Model.ModelID) == "" {
		return nil, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: resolved.Provider.ID,
			Message:  "google decision model requires ModelID",
		}
	}
	o := resolveOptions(opts)
	client, err := buildGenAIClient(ctx, resolved, o)
	if err != nil {
		return nil, err
	}
	return &DecisionModel{
		client:   client,
		provider: resolved.Provider.ID,
		modelID:  resolved.Model.ModelID,
		files:    newFileManager(client, resolved.Provider.ID, o),
	}, nil
}

func (m *DecisionModel) Decide(ctx context.Context, req ai.DecisionRequest) (ai.DecisionResult, error) {
	if len(req.Questions) == 0 {
		return ai.DecisionResult{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: m.provider,
			Message:  "at least one decision question is required",
		}
	}

	input, err := m.buildInput(ctx, req)
	if err != nil {
		return ai.DecisionResult{}, err
	}

	questions, err := m.buildQuestions(req.Questions)
	if err != nil {
		return ai.DecisionResult{}, err
	}

	resp, err := m.client.Decisions.Create(
		ctx,
		operations.CreateDecisionRequest{
			Body: decisions.CreateDecisionRequest{
				Model:     m.modelID,
				Input:     input,
				Questions: questions,
			},
		},
		operations.WithRetries(retry.Config{Strategy: "none"}),
	)
	if err != nil {
		return ai.DecisionResult{}, classifyError(m.provider, err)
	}
	if resp == nil || resp.CreateDecisionResponse == nil {
		return ai.DecisionResult{}, &ai.Error{
			Kind:     ai.ErrTransient,
			Provider: m.provider,
			Message:  "empty decisions response",
		}
	}

	answers := make(map[string]ai.DecisionAnswer, len(req.Questions))
	for id, q := range req.Questions {
		rawAns, ok := resp.CreateDecisionResponse.Answers[id]
		if !ok {
			return ai.DecisionResult{}, &ai.Error{
				Kind:     ai.ErrTransient,
				Provider: m.provider,
				Message:  fmt.Sprintf("decision response missing answer for %q", id),
			}
		}
		ans, err := mapDecisionAnswer(m.provider, id, q, rawAns)
		if err != nil {
			return ai.DecisionResult{}, err
		}
		answers[id] = ans
	}

	return ai.DecisionResult{
		Answers: answers,
		Usage:   mapUsage(resp.CreateDecisionResponse.Usage),
	}, nil
}

func (m *DecisionModel) buildInput(ctx context.Context, req ai.DecisionRequest) (decisions.Input, error) {
	if len(req.Parts) > 0 {
		blocks := make([]decisions.DecisionInputContent, 0, len(req.Parts))
		for _, p := range req.Parts {
			switch p.Kind {
			case ai.PartText:
				blocks = append(blocks, decisions.NewDecisionInputContent(interactions.TextContent{Text: p.Text}))
			case ai.PartImage, ai.PartAudio, ai.PartVideo:
				c, err := m.files.toInteractionContent(ctx, p.Kind, p.Media)
				if err != nil {
					return decisions.Input{}, err
				}
				switch {
				case c.ImageContent != nil:
					blocks = append(blocks, decisions.NewDecisionInputContent(*c.ImageContent))
				case c.AudioContent != nil:
					blocks = append(blocks, decisions.NewDecisionInputContent(*c.AudioContent))
				case c.VideoContent != nil:
					blocks = append(blocks, decisions.NewDecisionInputContent(*c.VideoContent))
				}
			default:
				return decisions.Input{}, &ai.Error{
					Kind:     ai.ErrInvalidArg,
					Provider: m.provider,
					Message:  fmt.Sprintf("unsupported decision input part kind %q", p.Kind),
				}
			}
		}
		return decisions.NewInput(blocks), nil
	}
	if strings.TrimSpace(req.Input) == "" {
		return decisions.Input{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: m.provider,
			Message:  "decision input is required",
		}
	}
	return decisions.NewInput(req.Input), nil
}

func (m *DecisionModel) buildQuestions(qs map[string]ai.DecisionQuestion) (map[string]decisions.Question, error) {
	out := make(map[string]decisions.Question, len(qs))
	for id, q := range qs {
		if strings.TrimSpace(q.Instructions) == "" {
			return nil, &ai.Error{
				Kind:     ai.ErrInvalidArg,
				Provider: m.provider,
				Message:  fmt.Sprintf("question %q requires instructions", id),
			}
		}
		switch q.Kind {
		case ai.DecisionNoul:
			nq := decisions.NoulQuestion{
				Instructions: decisions.NewNoulQuestionInstructions(q.Instructions),
			}
			if q.TrueDescription != "" || q.FalseDescription != "" {
				c := &decisions.NoulCriteria{}
				if q.TrueDescription != "" {
					td := q.TrueDescription
					c.TrueDescription = &td
				}
				if q.FalseDescription != "" {
					fd := q.FalseDescription
					c.FalseDescription = &fd
				}
				nq.Criteria = c
			}
			out[id] = decisions.NewQuestion(nq)

		case ai.DecisionScore:
			if len(q.Criteria) < 2 {
				return nil, &ai.Error{
					Kind:     ai.ErrInvalidArg,
					Provider: m.provider,
					Message:  fmt.Sprintf("score question %q requires at least 2 criteria levels", id),
				}
			}
			out[id] = decisions.NewQuestion(decisions.ScoreQuestion{
				Instructions: decisions.NewScoreQuestionInstructions(q.Instructions),
				Criteria:     q.Criteria,
			})

		case ai.DecisionChoice:
			if len(q.Choices) < 2 {
				return nil, &ai.Error{
					Kind:     ai.ErrInvalidArg,
					Provider: m.provider,
					Message:  fmt.Sprintf("choice question %q requires at least 2 choices", id),
				}
			}
			out[id] = decisions.NewQuestion(decisions.ChoiceQuestion{
				Instructions: decisions.NewChoiceQuestionInstructions(q.Instructions),
				Criteria:     q.Choices,
			})

		default:
			return nil, &ai.Error{
				Kind:     ai.ErrInvalidArg,
				Provider: m.provider,
				Message:  fmt.Sprintf("unsupported decision question kind %q", q.Kind),
			}
		}
	}
	return out, nil
}

func mapDecisionAnswer(provider, id string, q ai.DecisionQuestion, raw decisions.Answer) (ai.DecisionAnswer, error) {
	switch q.Kind {
	case ai.DecisionNoul:
		if raw.NoulAnswer == nil {
			return ai.DecisionAnswer{}, &ai.Error{
				Kind:     ai.ErrTransient,
				Provider: provider,
				Message:  fmt.Sprintf("question %q expected noul answer", id),
			}
		}
		return ai.DecisionAnswer{
			Kind:        ai.DecisionNoul,
			Probability: raw.NoulAnswer.Noul,
			Confidence:  raw.NoulAnswer.Confidence,
		}, nil

	case ai.DecisionScore:
		if raw.ScoreAnswer == nil {
			return ai.DecisionAnswer{}, &ai.Error{
				Kind:     ai.ErrTransient,
				Provider: provider,
				Message:  fmt.Sprintf("question %q expected score answer", id),
			}
		}
		return ai.DecisionAnswer{
			Kind:            ai.DecisionScore,
			Score:           raw.ScoreAnswer.Score,
			NormalizedScore: NormalizeScore(raw.ScoreAnswer.Score, len(q.Criteria)),
			Confidence:      raw.ScoreAnswer.Confidence,
			Probabilities:   maps.Clone(raw.ScoreAnswer.Probabilities),
			Legend:          maps.Clone(raw.ScoreAnswer.Legend),
		}, nil

	case ai.DecisionChoice:
		if raw.ChoiceAnswer == nil {
			return ai.DecisionAnswer{}, &ai.Error{
				Kind:     ai.ErrTransient,
				Provider: provider,
				Message:  fmt.Sprintf("question %q expected choice answer", id),
			}
		}
		return ai.DecisionAnswer{
			Kind:          ai.DecisionChoice,
			Choice:        raw.ChoiceAnswer.Choice,
			Confidence:    raw.ChoiceAnswer.Confidence,
			Probabilities: maps.Clone(raw.ChoiceAnswer.Probabilities),
		}, nil

	default:
		return ai.DecisionAnswer{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: provider,
			Message:  fmt.Sprintf("unsupported decision question kind %q", q.Kind),
		}
	}
}

// NormalizeScore maps an expected score in [0, levels-1] linearly into [-1, +1].
func NormalizeScore(score float64, levels int) float64 {
	if levels < 2 {
		return 0
	}
	v := (2.0 * score / float64(levels-1)) - 1.0
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}
