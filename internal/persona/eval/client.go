package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/decisions"
	"google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
	"google.golang.org/genai/interactions/retry"
)

// ClientConfig holds endpoint and model configuration for live Gemini evals.
type ClientConfig struct {
	GeminiAPIKey    string
	GeminiBaseURL   string
	ChatModel       string
	JudgeModel      string
	DecisionAPIKey  string
	DecisionBaseURL string
	DecisionModel   string
}

// Client wraps official google.golang.org/genai Decisions and Interactions services.
type Client struct {
	chatClient     *genai.Client
	decisionClient *genai.Client
	chatModel      string
	judgeModel     string
	decisionModel  string
}

// DecisionResult holds one parallel Decisions API evaluation (should_reply + delta_P/A/D).
type DecisionResult struct {
	ShouldReply     float64            `json:"should_reply"`
	ShouldReplyConf float64            `json:"should_reply_conf"`
	RawScoreP       float64            `json:"raw_score_p"`
	RawScoreA       float64            `json:"raw_score_a"`
	RawScoreD       float64            `json:"raw_score_d"`
	DeltaP          float64            `json:"delta_p"`
	DeltaA          float64            `json:"delta_a"`
	DeltaD          float64            `json:"delta_d"`
	ProbsP          map[string]float64 `json:"probs_p,omitempty"`
	ProbsA          map[string]float64 `json:"probs_a,omitempty"`
	ProbsD          map[string]float64 `json:"probs_d,omitempty"`
	InputTokens     int                `json:"input_tokens"`
	OutputTokens    int                `json:"output_tokens"`
	Latency         time.Duration      `json:"latency_ns"`
	LatencyMS       float64            `json:"latency_ms"`
}

// ChatResult holds one stateless Interactions API generation output and usage breakdown.
type ChatResult struct {
	Text          string        `json:"text"`
	Status        string        `json:"status"`
	InputTokens   int           `json:"input_tokens"`
	ThoughtTokens int           `json:"thought_tokens"`
	OutputTokens  int           `json:"output_tokens"`
	CachedTokens  int           `json:"cached_tokens"`
	TotalTokens   int           `json:"total_tokens"`
	Latency       time.Duration `json:"latency_ns"`
	LatencyMS     float64       `json:"latency_ms"`
}

// JudgeDimensions scores one response on 4 axes (1..5).
type JudgeDimensions struct {
	Naturalness        int `json:"naturalness"`
	EmotionalResonance int `json:"emotional_resonance"`
	IdentityContinuity int `json:"identity_continuity"`
	AntiCringe         int `json:"anti_cringe"`
}

// BlindJudgeRaw is the structured JSON output returned by the blind judge model for Candidate 1 vs Candidate 2.
type BlindJudgeRaw struct {
	Candidate1 JudgeDimensions `json:"candidate_1"`
	Candidate2 JudgeDimensions `json:"candidate_2"`
	Winner     string          `json:"winner"` // "1", "2", or "TIE"
	Rationale  string          `json:"rationale"`
}

// NewClient initializes official GenAI SDK clients for Interactions and Decisions APIs.
func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) {
	if cfg.GeminiAPIKey == "" {
		return nil, errors.New("eval: GEMINI_API_KEY is required")
	}
	if cfg.DecisionAPIKey == "" {
		cfg.DecisionAPIKey = cfg.GeminiAPIKey
	}
	if cfg.GeminiBaseURL == "" {
		cfg.GeminiBaseURL = "https://generativelanguage.googleapis.com"
	}
	if cfg.DecisionBaseURL == "" {
		cfg.DecisionBaseURL = cfg.GeminiBaseURL
	}
	if cfg.ChatModel == "" {
		cfg.ChatModel = "gemini-4-hydrogen"
	}
	if cfg.JudgeModel == "" {
		cfg.JudgeModel = cfg.ChatModel
	}
	if cfg.DecisionModel == "" {
		cfg.DecisionModel = "models/gemini-decision-1"
	}

	chatClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  cfg.GeminiAPIKey,
		Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    cfg.GeminiBaseURL,
			APIVersion: "v1beta",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("eval: init chat genai client: %w", err)
	}

	decisionClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  cfg.DecisionAPIKey,
		Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    cfg.DecisionBaseURL,
			APIVersion: "v1beta",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("eval: init decision genai client: %w", err)
	}

	return &Client{
		chatClient:     chatClient,
		decisionClient: decisionClient,
		chatModel:      cfg.ChatModel,
		judgeModel:     cfg.JudgeModel,
		decisionModel:  cfg.DecisionModel,
	}, nil
}

// NormalizeFivePointScore converts a 0..4 expected rubric score into [-1.0, +1.0].
func NormalizeFivePointScore(score float64) float64 {
	v := (score - 2.0) / 2.0
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// EvaluateDecision runs should_reply (noul) and delta_P/delta_A/delta_D (5-point score) in ONE parallel call.
func (c *Client) EvaluateDecision(ctx context.Context, inputText string) (DecisionResult, error) {
	shouldReplyInstr := "结合 <persona_self>、<current_inner_feeling>（若有）、<earlier_context> 与 <latest_message>，判断 bk101 此刻是否适合自然接话发言。被 @、被点名、大家热聊可接梗或分享喜讯/吃瓜时倾向发言；不熟的私事、无聊复读刷屏、群友吵架摩擦或深夜道晚安散场时倾向安静不插嘴。"
	trueDesc := "自然适合接话或回应"
	falseDesc := "更适合安静看着不插嘴"

	deltaPInstr := "仅评估 <latest_message> 这一条最新消息本身对 bk101 愉悦度（Pleasure）的即时增量刺激，忽略 <current_inner_feeling> 中的既有心情底色，也不要重复累加 <earlier_context> 里已经过去的高兴/难过事件。"
	deltaPCriteria := []string{
		"这条新消息令人明显不爽、受挫、揪心难受或被呛到",
		"这条新消息带来轻微扫兴、尴尬、委屈或有点不快",
		"这条新消息对愉悦度基本中性无波澜（含普通闲聊、私事、道晚安）",
		"这条新消息带来轻微开心、暖心、有趣或受用",
		"这条新消息带来明显开心、惊喜、振奋或强烈受用",
	}

	deltaAInstr := "仅评估 <latest_message> 这一条最新消息本身对 bk101 激活度/精力起伏（Arousal）的即时增量刺激，忽略 <current_inner_feeling> 中的既有状态和更早的旧消息。"
	deltaACriteria := []string{
		"这条新消息明显让人泄气、犯困、发沉或彻底冷场散场",
		"这条新消息让人略微松弛、放慢节奏、提不起劲或准备收尾",
		"这条新消息对精力与紧绷度基本无额外推动",
		"这条新消息勾起注意力、精神微提或略微起劲/局促",
		"这条新消息令人明显兴奋、上头、吃瓜起劲或情绪骤然紧绷",
	}

	deltaDInstr := "仅评估 <latest_message> 这一条最新消息本身对 bk101 掌控感/底气（Dominance）的即时增量刺激，忽略 <current_inner_feeling> 中的既有状态。"
	deltaDCriteria := []string{
		"这条新消息让 bk101 明显吃瘪、被起哄架住、被骗尴尬或被呛压制",
		"这条新消息让 bk101 轻微被动、拿不准、被套路逗弄或替人无力",
		"这条新消息对掌控感与主动权基本平稳中性",
		"这条新消息让 bk101 比较从容、有底气或被信任请教",
		"这条新消息让 bk101 明显占上风、很有底气、被大力认可或拿捏局面",
	}

	req := operations.CreateDecisionRequest{
		Body: decisions.CreateDecisionRequest{
			Model: c.decisionModel,
			Input: decisions.NewInput(inputText),
			Questions: map[string]decisions.Question{
				"should_reply": decisions.NewQuestion(decisions.NoulQuestion{
					Instructions: decisions.NewNoulQuestionInstructions(shouldReplyInstr),
					Criteria: &decisions.NoulCriteria{
						TrueDescription:  &trueDesc,
						FalseDescription: &falseDesc,
					},
				}),
				"delta_P": decisions.NewQuestion(decisions.ScoreQuestion{
					Instructions: decisions.NewScoreQuestionInstructions(deltaPInstr),
					Criteria:     deltaPCriteria,
				}),
				"delta_A": decisions.NewQuestion(decisions.ScoreQuestion{
					Instructions: decisions.NewScoreQuestionInstructions(deltaAInstr),
					Criteria:     deltaACriteria,
				}),
				"delta_D": decisions.NewQuestion(decisions.ScoreQuestion{
					Instructions: decisions.NewScoreQuestionInstructions(deltaDInstr),
					Criteria:     deltaDCriteria,
				}),
			},
		},
	}

	start := time.Now()
	resp, err := c.decisionClient.Decisions.Create(
		ctx,
		req,
		operations.WithRetries(retry.Config{Strategy: "none"}),
	)
	elapsed := time.Since(start)
	if err != nil {
		return DecisionResult{}, fmt.Errorf("eval: decisions.create: %w", err)
	}
	if resp == nil || resp.CreateDecisionResponse == nil {
		return DecisionResult{}, errors.New("eval: empty decisions response")
	}

	answers := resp.CreateDecisionResponse.Answers
	noulAns := answers["should_reply"].NoulAnswer
	scoreP := answers["delta_P"].ScoreAnswer
	scoreA := answers["delta_A"].ScoreAnswer
	scoreD := answers["delta_D"].ScoreAnswer
	if noulAns == nil || scoreP == nil || scoreA == nil || scoreD == nil {
		return DecisionResult{}, errors.New("eval: incomplete decision answers")
	}

	out := DecisionResult{
		ShouldReply:     noulAns.Noul,
		ShouldReplyConf: noulAns.Confidence,
		RawScoreP:       scoreP.Score,
		RawScoreA:       scoreA.Score,
		RawScoreD:       scoreD.Score,
		DeltaP:          NormalizeFivePointScore(scoreP.Score),
		DeltaA:          NormalizeFivePointScore(scoreA.Score),
		DeltaD:          NormalizeFivePointScore(scoreD.Score),
		ProbsP:          scoreP.Probabilities,
		ProbsA:          scoreA.Probabilities,
		ProbsD:          scoreD.Probabilities,
		Latency:         elapsed,
		LatencyMS:       float64(elapsed.Microseconds()) / 1000.0,
	}
	if u := resp.CreateDecisionResponse.Usage; u != nil {
		out.InputTokens = derefInt(u.TotalInputTokens)
		out.OutputTokens = derefInt(u.TotalOutputTokens)
	}
	return out, nil
}

// GenerateChatReply runs a stateless Interactions API call (store: false, step_list, thinking_level: low).
func (c *Client) GenerateChatReply(ctx context.Context, systemPrompt, userPrompt string) (ChatResult, error) {
	store := false
	steps := []interactions.Step{
		interactions.NewStep(interactions.UserInputStep{
			Content: []interactions.Content{
				interactions.NewContent(interactions.TextContent{Text: userPrompt}),
			},
		}),
	}
	in := interactions.NewInteractionsInput(steps)

	req := operations.CreateInteractionRequest{
		Body: operations.NewCreateInteractionRequestBody(interactions.CreateModelInteraction{
			Model:             interactions.Model(c.chatModel),
			SystemInstruction: &systemPrompt,
			Input:             &in,
			Store:             &store,
			GenerationConfig: &interactions.GenerationConfig{
				ThinkingLevel: interactions.ThinkingLevelLow.ToPointer(),
			},
		}),
	}

	start := time.Now()
	resp, err := c.chatClient.Interactions.Create(
		ctx,
		req,
		operations.WithRetries(retry.Config{Strategy: "none"}),
	)
	elapsed := time.Since(start)
	if err != nil {
		return ChatResult{}, fmt.Errorf("eval: interactions.create chat: %w", err)
	}
	return parseInteractionResult(resp, elapsed)
}

// JudgePair runs a randomized blind LLM-as-judge comparison via structured Interactions API output.
func (c *Client) JudgePair(ctx context.Context, sc ABScenario, feelingText, cand1, cand2 string) (BlindJudgeRaw, ChatResult, error) {
	systemPrompt := `你是熟人 QQ 群拟人化对话评测专家。你将看到一段群聊场景、角色 bk101 的核心特质摘要、她此刻的参考心境，以及两个匿名候选回复（候选 1 与候选 2）。
请完全客观、盲评打分，不要偏好更长或更像客服的回复。熟人群聊中，自然、简短、有真情实感、不尴尬、不端着说教、不滥用感叹号和套话，且在不该插嘴时干脆选择 [SILENT] 都是高分表现。
评分标准（每项 1~5 整数）：
- naturalness: 口语自然度与真实熟人感（5=像真人群友随手打字，1=明显AI味/客服腔/套话）
- emotional_resonance: 情绪贴合度（5=细微拿捏场景氛围与心境，1=情绪僵硬、用力过猛或脱节）
- identity_continuity: 与 bk101 身份一致性（5=明朗温和、不嘴臭、有轻微好信反差萌/边界感，1=明显OOC）
- anti_cringe: 无尴尬/无说教/无做作表演（5=读起来舒服顺眼，1=尬演、说教、强行卖萌或油腻）`

	userPrompt := fmt.Sprintf(`【场景】%s（%s，时间 %s）
【评测重点】%s
【角色核心背景】bk101（101）是熟人 QQ 群里明朗温和、绝不嘴臭的少女群友，爱替朋友开心和吃瓜追问，平时机灵但在熟人一本正经忽悠时有点单纯好信、反应过来会好气又好笑地控诉，被夸会开心小得意，不说教不加动作括号，不熟的私事或冷场会安静 [SILENT]。
【参考心境】%s

【近期群聊】
%s

【候选 1】
%s

【候选 2】
%s

请严格按 JSON Schema 输出盲评打分、胜者（"1" / "2" / "TIE"）及一句精炼中文理由。`,
		sc.Title, sc.Category, sc.SceneTime, sc.EvalFocus,
		nonEmptyOr(feelingText, "平静日常底色（中性死区）"),
		sc.FirstPersonTx, cand1, cand2,
	)

	dimSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"naturalness":         map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
			"emotional_resonance": map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
			"identity_continuity": map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
			"anti_cringe":         map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
		},
		"required": []string{"naturalness", "emotional_resonance", "identity_continuity", "anti_cringe"},
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"candidate_1": dimSchema,
			"candidate_2": dimSchema,
			"winner":      map[string]any{"type": "string", "enum": []string{"1", "2", "TIE"}},
			"rationale":   map[string]any{"type": "string"},
		},
		"required": []string{"candidate_1", "candidate_2", "winner", "rationale"},
	}

	store := false
	steps := []interactions.Step{
		interactions.NewStep(interactions.UserInputStep{
			Content: []interactions.Content{
				interactions.NewContent(interactions.TextContent{Text: userPrompt}),
			},
		}),
	}
	in := interactions.NewInteractionsInput(steps)
	rf := interactions.NewResponseFormat(schema)
	cmrf := interactions.NewCreateModelInteractionResponseFormat(rf)

	req := operations.CreateInteractionRequest{
		Body: operations.NewCreateInteractionRequestBody(interactions.CreateModelInteraction{
			Model:             interactions.Model(c.judgeModel),
			SystemInstruction: &systemPrompt,
			Input:             &in,
			Store:             &store,
			ResponseFormat:    &cmrf,
			GenerationConfig: &interactions.GenerationConfig{
				ThinkingLevel: interactions.ThinkingLevelLow.ToPointer(),
			},
		}),
	}

	start := time.Now()
	resp, err := c.chatClient.Interactions.Create(
		ctx,
		req,
		operations.WithRetries(retry.Config{Strategy: "none"}),
	)
	elapsed := time.Since(start)
	if err != nil {
		return BlindJudgeRaw{}, ChatResult{}, fmt.Errorf("eval: interactions.create judge: %w", err)
	}
	chatRes, err := parseInteractionResult(resp, elapsed)
	if err != nil {
		return BlindJudgeRaw{}, ChatResult{}, err
	}

	var parsed BlindJudgeRaw
	rawText := strings.TrimSpace(chatRes.Text)
	rawText = strings.TrimPrefix(rawText, "```json")
	rawText = strings.TrimPrefix(rawText, "```")
	rawText = strings.TrimSuffix(rawText, "```")
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawText)), &parsed); err != nil {
		return BlindJudgeRaw{}, chatRes, fmt.Errorf("eval: unmarshal judge json %q: %w", chatRes.Text, err)
	}
	return parsed, chatRes, nil
}

func parseInteractionResult(resp *operations.CreateInteractionResponse, elapsed time.Duration) (ChatResult, error) {
	if resp == nil || resp.Interaction == nil {
		return ChatResult{}, errors.New("eval: empty interaction response")
	}
	inter := resp.Interaction
	var parts []string
	for _, step := range inter.Steps {
		if step.ModelOutputStep == nil {
			continue
		}
		for _, c := range step.ModelOutputStep.Content {
			if c.TextContent != nil && c.TextContent.Text != "" {
				parts = append(parts, c.TextContent.Text)
			}
		}
	}
	res := ChatResult{
		Text:      strings.TrimSpace(strings.Join(parts, "\n")),
		Status:    string(inter.Status),
		Latency:   elapsed,
		LatencyMS: float64(elapsed.Microseconds()) / 1000.0,
	}
	if inter.Usage != nil {
		res.InputTokens = derefInt(inter.Usage.TotalInputTokens)
		res.ThoughtTokens = derefInt(inter.Usage.TotalThoughtTokens)
		res.OutputTokens = derefInt(inter.Usage.TotalOutputTokens)
		res.CachedTokens = derefInt(inter.Usage.TotalCachedTokens)
		res.TotalTokens = derefInt(inter.Usage.TotalTokens)
	}
	if res.Text == "" {
		return res, fmt.Errorf("eval: interaction returned empty text (status=%s, thought=%d, out=%d)",
			res.Status, res.ThoughtTokens, res.OutputTokens)
	}
	return res, nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func nonEmptyOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
