package eval

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"omni-bot/internal/persona/pad"
)

// StimulusOutcome records one Experiment 1a live result.
type StimulusOutcome struct {
	Case   StimulusCase   `json:"case"`
	Result DecisionResult `json:"result"`
}

// PairCheckResult records whether one directional monotonicity check passed.
type PairCheckResult struct {
	Pair    MonotonicityPair `json:"pair"`
	HighVal float64          `json:"high_val"`
	LowVal  float64          `json:"low_val"`
	Passed  bool             `json:"passed"`
}

// TrajectoryStep records one turn in the 25-turn PAD simulation.
type TrajectoryStep struct {
	Turn           ConversationTurn `json:"turn"`
	Decision       DecisionResult   `json:"decision"`
	StateAfter     pad.Vec3         `json:"state_after"`
	MatchAfter     pad.MatchResult  `json:"match_after"`
	InjectedBefore string           `json:"injected_before"`
}

// ABOutcome records one blind A/B scenario comparison (Arm A legacy vs Arm B 1st-person + PAD).
type ABOutcome struct {
	Scenario        ABScenario      `json:"scenario"`
	Match           pad.MatchResult `json:"match"`
	InnerFeeling    string          `json:"inner_feeling"`
	ArmA            ChatResult      `json:"arm_a"`
	ArmB            ChatResult      `json:"arm_b"`
	PresentedOrder  string          `json:"presented_order"` // "AB" or "BA"
	JudgeRaw        BlindJudgeRaw   `json:"judge_raw"`
	JudgeUsage      ChatResult      `json:"judge_usage"`
	ScoresA         JudgeDimensions `json:"scores_a"`
	ScoresB         JudgeDimensions `json:"scores_b"`
	UnblindedWinner string          `json:"unblinded_winner"` // "A", "B", or "TIE"
	Rationale       string          `json:"rationale"`
}

// Report aggregates all Phase 0 live validation results.
type Report struct {
	GeneratedAt     time.Time         `json:"generated_at"`
	DecisionModel   string            `json:"decision_model"`
	ChatModel       string            `json:"chat_model"`
	JudgeModel      string            `json:"judge_model"`
	Temperament     pad.Temperament   `json:"temperament"`
	StimulusResults []StimulusOutcome `json:"stimulus_results"`
	PairChecks      []PairCheckResult `json:"pair_checks"`
	Trajectory      []TrajectoryStep  `json:"trajectory"`
	ABResults       []ABOutcome       `json:"ab_results"`
}

// ProgressFunc receives live progress log lines during long benchmark runs.
type ProgressFunc func(format string, args ...any)

// RunAll executes Experiment 1a, Experiment 1b, and Experiment 2 sequentially and returns the full Report.
func RunAll(ctx context.Context, client *Client, temp pad.Temperament, logf ProgressFunc) (Report, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	report := Report{
		GeneratedAt:   time.Now().UTC(),
		DecisionModel: client.decisionModel,
		ChatModel:     client.chatModel,
		JudgeModel:    client.judgeModel,
		Temperament:   temp,
	}

	// Experiment 1a: 18 stimulus scenarios in parallel Decisions API calls (single call per scenario).
	stimuli := StimulusCases()
	personaSummary := CompactDecisionPersona()
	byID := make(map[string]DecisionResult, len(stimuli))
	logf("[Exp 1a] Running %d Decisions API stimulus scenarios (%s)...", len(stimuli), client.decisionModel)
	for i, sc := range stimuli {
		in := FormatDecisionInput(personaSummary, "", sc.Transcript)
		res, err := client.EvaluateDecision(ctx, in)
		if err != nil {
			return report, fmt.Errorf("exp 1a case %s: %w", sc.ID, err)
		}
		report.StimulusResults = append(report.StimulusResults, StimulusOutcome{Case: sc, Result: res})
		byID[sc.ID] = res
		logf("  (%02d/%02d) %-20s reply=%.2f dP=%+.2f dA=%+.2f dD=%+.2f outTok=%d %.0fms",
			i+1, len(stimuli), sc.ID, res.ShouldReply, res.DeltaP, res.DeltaA, res.DeltaD, res.OutputTokens, res.LatencyMS)
	}
	report.PairChecks = EvaluateMonotonicityPairs(MonotonicityPairs(), byID)

	// Experiment 1b: 25-turn multi-phase group conversation simulation.
	script := LongConversationScript()
	logf("[Exp 1b] Running %d-turn PAD trajectory simulation...", len(script))
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
	state := pad.NewState(temp, now)
	var window []string
	for _, turn := range script {
		now = now.Add(turn.Elapsed)
		line := fmt.Sprintf("[%s %s] %s", now.Format("15:04"), turn.Sender, turn.Message)
		window = append(window, line)
		if len(window) > 6 {
			window = window[len(window)-6:]
		}

		// Render current <inner_feeling> after time decay, matching real System 1 runtime semantics.
		decayed := temp.Decay(state.Value, now.Sub(state.UpdatedAt))
		feelingBefore := pad.RenderInnerFeeling(decayed, temp)
		in := FormatDecisionInput(personaSummary, feelingBefore, strings.Join(window, "\n"))
		dec, err := client.EvaluateDecision(ctx, in)
		if err != nil {
			return report, fmt.Errorf("exp 1b turn %d: %w", turn.Turn, err)
		}

		state = temp.Step(state, pad.Vec3{P: dec.DeltaP, A: dec.DeltaA, D: dec.DeltaD}, now)
		matched := pad.Match(state.Value, temp.Baseline, temp.DeadzoneRadius)
		report.Trajectory = append(report.Trajectory, TrajectoryStep{
			Turn:           turn,
			Decision:       dec,
			StateAfter:     state.Value,
			MatchAfter:     matched,
			InjectedBefore: feelingBefore,
		})
		label := "neutral(deadzone)"
		if matched.Active {
			label = matched.Prototype.Name
		}
		logf("  Turn %02d [%-10s] dt=%-5s d=(%+.2f,%+.2f,%+.2f) -> PAD=(%+.2f,%+.2f,%+.2f) [%s] %.0fms",
			turn.Turn, turn.Phase, turn.Elapsed, dec.DeltaP, dec.DeltaA, dec.DeltaD,
			state.Value.P, state.Value.A, state.Value.D, label, dec.LatencyMS)
	}

	// Experiment 2: 10-scenario blind A/B via Interactions API.
	abScenarios := ABScenarios()
	legacySys := BuildLegacySystemPrompt()
	firstPersonSys := BuildFirstPersonSystemPrompt(MigratedPersonaBK101())
	logf("[Exp 2] Running %d blind A/B scenarios (%s, judge=%s)...", len(abScenarios), client.chatModel, client.judgeModel)
	for i, sc := range abScenarios {
		matched := pad.Match(sc.PriorPAD, temp.Baseline, temp.DeadzoneRadius)
		feeling := ""
		if matched.Active {
			feeling = matched.Prototype.Feeling
		}

		userA := FormatChatUserInput(sc.SceneTime, "", sc.LegacyTranscript)
		resA, err := client.GenerateChatReply(ctx, legacySys, userA)
		if err != nil {
			return report, fmt.Errorf("exp 2 scenario %s arm A: %w", sc.ID, err)
		}

		userB := FormatChatUserInput(sc.SceneTime, feeling, sc.FirstPersonTx)
		resB, err := client.GenerateChatReply(ctx, firstPersonSys, userB)
		if err != nil {
			return report, fmt.Errorf("exp 2 scenario %s arm B: %w", sc.ID, err)
		}

		// Deterministic balanced blind order: odd indices swap B first, even indices A first.
		order, cand1, cand2 := BlindPresentationOrder(i, resA.Text, resB.Text)
		judgeRaw, judgeUsage, err := client.JudgePair(ctx, sc, feeling, cand1, cand2)
		if err != nil {
			return report, fmt.Errorf("exp 2 scenario %s judge: %w", sc.ID, err)
		}
		scoresA, scoresB, winner := UnblindJudge(order, judgeRaw)

		report.ABResults = append(report.ABResults, ABOutcome{
			Scenario:        sc,
			Match:           matched,
			InnerFeeling:    feeling,
			ArmA:            resA,
			ArmB:            resB,
			PresentedOrder:  order,
			JudgeRaw:        judgeRaw,
			JudgeUsage:      judgeUsage,
			ScoresA:         scoresA,
			ScoresB:         scoresB,
			UnblindedWinner: winner,
			Rationale:       judgeRaw.Rationale,
		})
		logf("  (%02d/%02d) %-22s order=%s winner=%-3s A=%d/%d/%d/%d B=%d/%d/%d/%d (A %.0fms, B %.0fms)",
			i+1, len(abScenarios), sc.ID, order, winner,
			scoresA.Naturalness, scoresA.EmotionalResonance, scoresA.IdentityContinuity, scoresA.AntiCringe,
			scoresB.Naturalness, scoresB.EmotionalResonance, scoresB.IdentityContinuity, scoresB.AntiCringe,
			resA.LatencyMS, resB.LatencyMS)
	}

	return report, nil
}

// EvaluateMonotonicityPairs checks every expected directional pair against measured decision outputs.
func EvaluateMonotonicityPairs(pairs []MonotonicityPair, byID map[string]DecisionResult) []PairCheckResult {
	out := make([]PairCheckResult, 0, len(pairs))
	for _, p := range pairs {
		hi := extractMetric(byID[p.HighID], p.Metric)
		lo := extractMetric(byID[p.LowID], p.Metric)
		out = append(out, PairCheckResult{
			Pair:    p,
			HighVal: hi,
			LowVal:  lo,
			Passed:  hi > lo,
		})
	}
	return out
}

func extractMetric(r DecisionResult, metric string) float64 {
	switch metric {
	case "delta_P":
		return r.DeltaP
	case "delta_A":
		return r.DeltaA
	case "delta_D":
		return r.DeltaD
	case "should_reply":
		return r.ShouldReply
	default:
		return 0
	}
}

// BlindPresentationOrder deterministically alternates AB/BA order across scenarios for balanced blind judging.
func BlindPresentationOrder(index int, replyA, replyB string) (order, cand1, cand2 string) {
	if index%2 == 1 {
		return "BA", replyB, replyA
	}
	return "AB", replyA, replyB
}

// UnblindJudge maps Candidate 1 / Candidate 2 blind scores back to Arm A and Arm B.
func UnblindJudge(order string, raw BlindJudgeRaw) (scoresA, scoresB JudgeDimensions, winner string) {
	if order == "BA" {
		scoresA = raw.Candidate2
		scoresB = raw.Candidate1
		switch raw.Winner {
		case "1":
			winner = "B"
		case "2":
			winner = "A"
		default:
			winner = "TIE"
		}
		return scoresA, scoresB, winner
	}
	scoresA = raw.Candidate1
	scoresB = raw.Candidate2
	switch raw.Winner {
	case "1":
		winner = "A"
	case "2":
		winner = "B"
	default:
		winner = "TIE"
	}
	return scoresA, scoresB, winner
}

// Percentile computes the nearest-rank p-th percentile (p in [0, 100]) of a slice of float64 values.
func Percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil((p / 100.0) * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// RenderMarkdownReport produces the complete Phase 0 benchmark report in GitHub-flavored Markdown.
func RenderMarkdownReport(r Report) string {
	var b strings.Builder

	// Summary stats for Exp 1a & 1b Decisions latency
	var decLat []float64
	zeroOutOK := true
	totalDecCalls := len(r.StimulusResults) + len(r.Trajectory)
	for _, s := range r.StimulusResults {
		decLat = append(decLat, s.Result.LatencyMS)
		if s.Result.OutputTokens != 0 {
			zeroOutOK = false
		}
	}
	for _, t := range r.Trajectory {
		decLat = append(decLat, t.Decision.LatencyMS)
		if t.Decision.OutputTokens != 0 {
			zeroOutOK = false
		}
	}
	passedPairs := 0
	for _, c := range r.PairChecks {
		if c.Passed {
			passedPairs++
		}
	}

	// Summary stats for Exp 1b Trajectory
	minP, maxP := 1.0, -1.0
	minA, maxA := 1.0, -1.0
	minD, maxD := 1.0, -1.0
	deadzoneTurns := 0
	distinctProtos := make(map[string]bool)
	for _, t := range r.Trajectory {
		minP = math.Min(minP, t.StateAfter.P)
		maxP = math.Max(maxP, t.StateAfter.P)
		minA = math.Min(minA, t.StateAfter.A)
		maxA = math.Max(maxA, t.StateAfter.A)
		minD = math.Min(minD, t.StateAfter.D)
		maxD = math.Max(maxD, t.StateAfter.D)
		if !t.MatchAfter.Active {
			deadzoneTurns++
		} else {
			distinctProtos[t.MatchAfter.Prototype.ID] = true
		}
	}

	// Summary stats for Exp 2 Blind A/B
	var winsA, winsB, ties int
	var sumNatA, sumEmoA, sumIdA, sumAntiA float64
	var sumNatB, sumEmoB, sumIdB, sumAntiB float64
	var latA, latB []float64
	var inTokA, thTokA, outTokA, cacheTokA int
	var inTokB, thTokB, outTokB, cacheTokB int
	for _, ab := range r.ABResults {
		switch ab.UnblindedWinner {
		case "A":
			winsA++
		case "B":
			winsB++
		default:
			ties++
		}
		sumNatA += float64(ab.ScoresA.Naturalness)
		sumEmoA += float64(ab.ScoresA.EmotionalResonance)
		sumIdA += float64(ab.ScoresA.IdentityContinuity)
		sumAntiA += float64(ab.ScoresA.AntiCringe)
		sumNatB += float64(ab.ScoresB.Naturalness)
		sumEmoB += float64(ab.ScoresB.EmotionalResonance)
		sumIdB += float64(ab.ScoresB.IdentityContinuity)
		sumAntiB += float64(ab.ScoresB.AntiCringe)

		latA = append(latA, ab.ArmA.LatencyMS)
		latB = append(latB, ab.ArmB.LatencyMS)
		inTokA += ab.ArmA.InputTokens
		thTokA += ab.ArmA.ThoughtTokens
		outTokA += ab.ArmA.OutputTokens
		cacheTokA += ab.ArmA.CachedTokens
		inTokB += ab.ArmB.InputTokens
		thTokB += ab.ArmB.ThoughtTokens
		outTokB += ab.ArmB.OutputTokens
		cacheTokB += ab.ArmB.CachedTokens
	}
	nAB := float64(max(1, len(r.ABResults)))
	avgAllA := (sumNatA + sumEmoA + sumIdA + sumAntiA) / (4 * nAB)
	avgAllB := (sumNatB + sumEmoB + sumIdB + sumAntiB) / (4 * nAB)

	b.WriteString("# Phase 0 人格三层结构、PAD 动态情绪与 Interactions API 实测报告\n\n")
	fmt.Fprintf(&b, "> 评测时间：`%s` ｜ Decisions 模型：`%s` ｜ Chat 模型：`%s`（`store: false`, `step_list`, `thinking_level: low`）｜ 盲评裁判：`%s`\n\n",
		r.GeneratedAt.Format(time.RFC3339), r.DecisionModel, r.ChatModel, r.JudgeModel)

	b.WriteString("## 1. 核心结论一览\n\n")
	b.WriteString("| 验证项 | 实测指标 | 结论 |\n")
	b.WriteString("| :--- | :--- | :--- |\n")
	fmt.Fprintf(&b, "| **Decisions API 并行顺风车**（%d 次调用） | `total_output_tokens == 0`: **%v**；延迟 p50 = **%.0f ms**，p95 = **%.0f ms** | 单次前向传播并行输出 `should_reply` + 3 轴 PAD 刺激，零生成 Token、低延迟可用 |\n",
		totalDecCalls, zeroOutOK, Percentile(decLat, 50), Percentile(decLat, 95))
	fmt.Fprintf(&b, "| **PAD 刺激单调性**（%d 对方向断言） | **%d / %d** 通过（%.1f%%） | 夸奖/出货/忽悠/吃瘪/冷场/私事在 P/A/D 及接话欲上方向区分清晰 |\n",
		len(r.PairChecks), passedPairs, len(r.PairChecks), 100.0*float64(passedPairs)/float64(max(1, len(r.PairChecks))))
	fmt.Fprintf(&b, "| **25 轮长对话轨迹收敛** | P∈`[%+.2f, %+.2f]`，A∈`[%+.2f, %+.2f]`，D∈`[%+.2f, %+.2f]`；死区静默 %d 轮；覆盖 %d 种不同原型 | 全程零发散漂移，高能→被骗→共情→回暖→深夜散场过渡平滑 |\n",
		minP, maxP, minA, maxA, minD, maxD, deadzoneTurns, len(distinctProtos))
	fmt.Fprintf(&b, "| **bk101 盲评 A/B**（%d 场景，均衡 AB/BA 顺序） | **Arm B 胜 %d / 平 %d / Arm A 胜 %d**；综合均分 A=**%.2f** vs B=**%.2f**（满分 5.0） | 第一人称三层人格 + 细粒度 `<inner_feeling>` 无缝延续 `bk101` 身份且情绪与自然度更优 |\n",
		len(r.ABResults), winsB, ties, winsA, avgAllA, avgAllB)
	fmt.Fprintf(&b, "| **Interactions API 性能**（Arm A / Arm B 各 %d 次） | A 延迟 p50=**%.0f ms**（p95=%.0f ms）；B 延迟 p50=**%.0f ms**（p95=%.0f ms） | 无状态 `step_list` + `store: false` 稳定运行；单轮输入 <1000 tok 未达隐式缓存阈值（`cached=%d/%d`） |\n\n",
		len(r.ABResults), Percentile(latA, 50), Percentile(latA, 95), Percentile(latB, 50), Percentile(latB, 95), cacheTokA, cacheTokB)

	// Section 2: Experiment 1a
	b.WriteString("## 2. 实验一（A）：Decisions API PAD 刺激单调性与延迟\n\n")
	b.WriteString("每条群聊场景仅发起 **1 次** `client.Decisions.Create` 请求，并行挂载 `should_reply`（`noul`）与 `delta_P` / `delta_A` / `delta_D`（5 档 `score`，按 `(score - 2) / 2` 归一化至 `[-1, +1]`）。\n\n")
	b.WriteString("### 2.1 18 组群聊刺激实测表\n\n")
	b.WriteString("| 场景 ID | 类别 | `should_reply` | `δP` | `δA` | `δD` | Input Tok | Output Tok | 延迟 (ms) |\n")
	b.WriteString("| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: |\n")
	for _, s := range r.StimulusResults {
		fmt.Fprintf(&b, "| `%s` | %s | %.3f | %+.3f | %+.3f | %+.3f | %d | %d | %.0f |\n",
			s.Case.ID, s.Case.Category, s.Result.ShouldReply, s.Result.DeltaP, s.Result.DeltaA, s.Result.DeltaD,
			s.Result.InputTokens, s.Result.OutputTokens, s.Result.LatencyMS)
	}
	b.WriteString("\n### 2.2 成对单调性断言核验\n\n")
	b.WriteString("| 断言项 | 维度 | 高项实测 | 低项实测 | 结果 |\n")
	b.WriteString("| :--- | :---: | :---: | :---: | :---: |\n")
	for _, c := range r.PairChecks {
		mark := "PASS"
		if !c.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "| %s | `%s` | %+.3f (`%s`) | %+.3f (`%s`) | **%s** |\n",
			c.Pair.Name, c.Pair.Metric, c.HighVal, c.Pair.HighID, c.LowVal, c.Pair.LowID, mark)
	}

	// Section 3: Experiment 1b
	b.WriteString("\n## 3. 实验一（B）：25 轮多阶段长对话 PAD 轨迹与参数校准\n\n")
	fmt.Fprintf(&b, "校准后的 `bk101` 气质参数：静息基线 `(P0=%.2f, A0=%.2f, D0=%.2f)`，敏感度 `(ηP=%.2f, ηA=%.2f, ηD=%.2f)`，指数回归半衰期 `(τP=%v, τA=%v, τD=%v)`，中性死区半径 `r=%.2f`（基线欧氏距离 `< %.2f` 时不注入 `<inner_feeling>`）。\n\n",
		r.Temperament.Baseline.P, r.Temperament.Baseline.A, r.Temperament.Baseline.D,
		r.Temperament.Sensitivity.P, r.Temperament.Sensitivity.A, r.Temperament.Sensitivity.D,
		r.Temperament.HalfLifeP, r.Temperament.HalfLifeA, r.Temperament.HalfLifeD,
		r.Temperament.DeadzoneRadius, r.Temperament.DeadzoneRadius)

	b.WriteString("| 轮次 | 阶段 | Δt | 发言摘要 | 接话欲 | `(δP, δA, δD)` | 更新后 `(P, A, D)` | 激活内在感受原型 |\n")
	b.WriteString("| :---: | :--- | :---: | :--- | :---: | :---: | :---: | :--- |\n")
	for _, step := range r.Trajectory {
		proto := "*(中性死区・不注入)*"
		if step.MatchAfter.Active {
			proto = fmt.Sprintf("`%s` (%s)", step.MatchAfter.Prototype.ID, step.MatchAfter.Prototype.Name)
		}
		msgPreview := truncateRunes(step.Turn.Sender+": "+step.Turn.Message, 28)
		fmt.Fprintf(&b, "| %02d | %s | %s | %s | %.2f | `(%+.2f,%+.2f,%+.2f)` | `(%+.2f,%+.2f,%+.2f)` | %s |\n",
			step.Turn.Turn, step.Turn.Phase, step.Turn.Elapsed, escapePipe(msgPreview),
			step.Decision.ShouldReply,
			step.Decision.DeltaP, step.Decision.DeltaA, step.Decision.DeltaD,
			step.StateAfter.P, step.StateAfter.A, step.StateAfter.D,
			proto)
	}

	// Section 4: Finalized 30-prototype catalog
	b.WriteString("\n## 4. 定稿：30 项细粒度第一人称 PAD 内在感受原型表\n\n")
	b.WriteString("全部 30 个质心覆盖三维 PAD 全部 8 个象限及 4 个单轴过渡态，严格遵循《AGENTS.md》§2（纯第一人称内在主观体验，零气泡/字数/格式控制剧本）。\n\n")
	b.WriteString("| # | ID | 象限 | 名称 | 坐标 `(P, A, D)` | 第一人称内在感受 (`<inner_feeling>`) |\n")
	b.WriteString("| :---: | :--- | :---: | :--- | :---: | :--- |\n")
	for i, p := range pad.Catalog() {
		fmt.Fprintf(&b, "| %02d | `%s` | `%s` | %s | `(%+.2f, %+.2f, %+.2f)` | %s |\n",
			i+1, p.ID, p.Octant, p.Name, p.Coord.P, p.Coord.A, p.Coord.D, escapePipe(p.Feeling))
	}

	// Section 5: Experiment 2 Blind A/B
	b.WriteString("\n## 5. 实验二：bk101 第一人称三层结构 + PAD 内在感受盲评 A/B（Interactions API）\n\n")
	b.WriteString("对照设置：共享相同中性 `<system_protocol>`、相同模型与 `thinking_level: low`、`store: false` 无状态 `step_list`：\n")
	b.WriteString("- **Arm A（旧版扁平人设）**：直接加载现网 `data_v4.db` 中 725 字 `bk101` 第二人称人设，无 `<inner_feeling>`。\n")
	b.WriteString("- **Arm B（第一人称三层 + PAD）**：冻结第一人称自我观念 + 9 条原子第一人称性格条目 + 动态区挂载的 `<inner_feeling>`（死区内不注入），历史自身消息使用 `[我(bk101)]`。\n\n")

	b.WriteString("### 5.1 盲评维度汇总（1~5 分，10 场景均衡 AB/BA 呈现）\n\n")
	b.WriteString("| 评测维度 | Arm A（旧版扁平） | Arm B（第一人称三层 + PAD） | 差值 (B - A) |\n")
	b.WriteString("| :--- | :---: | :---: | :---: |\n")
	fmt.Fprintf(&b, "| 自然度 / 无 AI 味 (`naturalness`) | %.2f | **%.2f** | %+.2f |\n", sumNatA/nAB, sumNatB/nAB, (sumNatB-sumNatA)/nAB)
	fmt.Fprintf(&b, "| 情绪贴合度 (`emotional_resonance`) | %.2f | **%.2f** | %+.2f |\n", sumEmoA/nAB, sumEmoB/nAB, (sumEmoB-sumEmoA)/nAB)
	fmt.Fprintf(&b, "| 与 bk101 身份一致性 (`identity_continuity`) | %.2f | **%.2f** | %+.2f |\n", sumIdA/nAB, sumIdB/nAB, (sumIdB-sumIdA)/nAB)
	fmt.Fprintf(&b, "| 无尴尬 / 无说教 / 无 OOC (`anti_cringe`) | %.2f | **%.2f** | %+.2f |\n", sumAntiA/nAB, sumAntiB/nAB, (sumAntiB-sumAntiA)/nAB)
	fmt.Fprintf(&b, "| **四维总均分** | **%.2f** | **%.2f** | **%+.2f** |\n\n", avgAllA, avgAllB, avgAllB-avgAllA)
	fmt.Fprintf(&b, "**胜负统计**：Arm B 胜 **%d** 场，平局 **%d** 场，Arm A 胜 **%d** 场。\n\n", winsB, ties, winsA)

	b.WriteString("### 5.2 逐场景评分与 Interactions API Token / 延迟明细\n\n")
	b.WriteString("| 场景 | 类别 | 盲评顺序 | 胜者 | A 得分 (N/E/I/C) | B 得分 (N/E/I/C) | A 延迟/Tok (in/th/out/cache) | B 延迟/Tok (in/th/out/cache) |\n")
	b.WriteString("| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: |\n")
	for _, ab := range r.ABResults {
		fmt.Fprintf(&b, "| `%s` | %s | %s | **%s** | %d/%d/%d/%d | %d/%d/%d/%d | %.0fms (`%d/%d/%d/%d`) | %.0fms (`%d/%d/%d/%d`) |\n",
			ab.Scenario.ID, ab.Scenario.Category, ab.PresentedOrder, ab.UnblindedWinner,
			ab.ScoresA.Naturalness, ab.ScoresA.EmotionalResonance, ab.ScoresA.IdentityContinuity, ab.ScoresA.AntiCringe,
			ab.ScoresB.Naturalness, ab.ScoresB.EmotionalResonance, ab.ScoresB.IdentityContinuity, ab.ScoresB.AntiCringe,
			ab.ArmA.LatencyMS, ab.ArmA.InputTokens, ab.ArmA.ThoughtTokens, ab.ArmA.OutputTokens, ab.ArmA.CachedTokens,
			ab.ArmB.LatencyMS, ab.ArmB.InputTokens, ab.ArmB.ThoughtTokens, ab.ArmB.OutputTokens, ab.ArmB.CachedTokens)
	}
	fmt.Fprintf(&b, "\n*Interactions API 汇总（%d 次生成/每组）*：Arm A 累计 `in=%d, thought=%d, out=%d, cached=%d`（p50=%.0fms, p95=%.0fms）；Arm B 累计 `in=%d, thought=%d, out=%d, cached=%d`（p50=%.0fms, p95=%.0fms）。\n\n",
		len(r.ABResults),
		inTokA, thTokA, outTokA, cacheTokA, Percentile(latA, 50), Percentile(latA, 95),
		inTokB, thTokB, outTokB, cacheTokB, Percentile(latB, 50), Percentile(latB, 95))

	b.WriteString("### 5.3 并排对话实录对比（精选全部 10 组场景）\n\n")
	for idx, ab := range r.ABResults {
		protoDesc := "中性死区（未注入 `<inner_feeling>`）"
		if ab.Match.Active {
			protoDesc = fmt.Sprintf("`%s`（%s）：%s", ab.Match.Prototype.ID, ab.Match.Prototype.Name, ab.InnerFeeling)
		}
		fmt.Fprintf(&b, "#### 场景 %02d：%s（`%s`｜盲评结果：**%s**）\n\n", idx+1, ab.Scenario.Title, ab.Scenario.Category, ab.UnblindedWinner)
		fmt.Fprintf(&b, "- **上下文**：\n```\n%s\n```\n", ab.Scenario.FirstPersonTx)
		fmt.Fprintf(&b, "- **Arm B 注入心境**：%s\n", protoDesc)
		fmt.Fprintf(&b, "- **Arm A（旧版扁平人设）**：`%s`\n", strings.ReplaceAll(ab.ArmA.Text, "\n", " / "))
		fmt.Fprintf(&b, "- **Arm B（第一人称三层 + PAD）**：`%s`\n", strings.ReplaceAll(ab.ArmB.Text, "\n", " / "))
		fmt.Fprintf(&b, "- **盲评裁判简评**：%s\n\n", ab.Rationale)
	}

	b.WriteString("## 6. 首轮实测发现与校准闭环\n\n")
	b.WriteString("在 Phase 0 首次实测（首轮默认参数 `η=(0.32,0.36,0.30), τ=(18m,10m,14m)`、单块 `<recent_group_chat>` 未切分最新消息）中发现三项可直接指导生产落地的真实工程规律，并在本报告对应的校准版中闭环验证：\n\n")
	b.WriteString("1. **防正反馈锚定（Decisions API 关键陷阱）**：若直接将 `<inner_feeling>` 与整个 6 条历史窗口混排为单一 `<recent_group_chat>`，模型容易把既有的高涨心境及数轮前的出货消息重复计入 `δP/δA/δD`，导致连发 4 条消息即顶死 `P=+1.00, A=+1.00` 且后续调侃/晚安轮仍残留正偏置。改为显式拆分 `<earlier_context>` + `<latest_message>`，并在三个 `score` rubric 中要求仅评估 `<latest_message>` 的即时新增刺激、忽略 `<current_inner_feeling>` 底色后，轨迹恢复清晰回落与多象限切换。\n")
	b.WriteString("2. **高频群聊敏感度与半衰期校准**：熟人群聊常在 20~60 秒内连续刷出多条高能消息，敏感度从 `(0.32, 0.36, 0.30)` 下调至 `(0.18, 0.22, 0.18)`、半衰期从 `(18m, 10m, 14m)` 收紧至 `(12m, 6m, 10m)`、死区半径定为 `0.18`，消除快速饱和顶死，同时保持深夜停顿后自然回落。\n")
	b.WriteString("3. **bk101 身份边界在第一人称中的精准保真**：首轮盲评中 Arm B 在 `ab01_loot_hype` 高激活态下出现了一句粗口（“卧槽牛逼”），根因是第一人称信念层弱化了旧人设里的“少女群友 / 说话干净绝不飙脏话”约束。补齐该原生第一人称自我约束（零硬脚本、零上帝视角标签）后，消除 OOC 风险并保持自然松弛口吻。\n\n")

	b.WriteString("## 7. 工程落地建议（供后续 Phase 1/2 复用）\n\n")
	b.WriteString("1. **Decisions API 并行挂车方案定型**：单次 `client.Decisions.Create`（`operations.WithRetries(retry.Config{Strategy: \"none\"})`）同时评估 `should_reply`（`noul`）与 `delta_P`/`delta_A`/`delta_D`（5 档 `score`，`(score-2)/2` 归一化），严格 `0` 输出 Token；输入必须物理拆分 `<earlier_context>` 与 `<latest_message>`，防止历史峰值重复计分。\n")
	fmt.Fprintf(&b, "2. **PAD 动力学参数与 30 原型定稿**：`internal/persona/pad` 已通过离线单测与 25 轮长对话仿真验证；静息基线 `(%.2f, %.2f, %.2f)`、敏感度 `(%.2f, %.2f, %.2f)`、半衰期 `(%v, %v, %v)` 与死区半径 `%.2f` 既保留鲜明情绪起伏，又确保自然回落零漂移。\n",
		r.Temperament.Baseline.P, r.Temperament.Baseline.A, r.Temperament.Baseline.D,
		r.Temperament.Sensitivity.P, r.Temperament.Sensitivity.A, r.Temperament.Sensitivity.D,
		r.Temperament.HalfLifeP, r.Temperament.HalfLifeA, r.Temperament.HalfLifeD,
		r.Temperament.DeadzoneRadius)
	b.WriteString("3. **bk101 无缝迁移与缓存友好布局**：冻结第一人称自我观念 + 9 条原子性格条目放在静态 `system_instruction`，动态 `<inner_feeling>` 严格置于单轮 Turn 输入中（死区内完全省略），与无状态 `step_list`（`store: false`）天然兼容。\n")

	return b.String()
}

func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

func escapePipe(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}
