package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"omni-bot/internal/envload"
	"omni-bot/internal/memory/eval"
)

func main() {
	envPath := flag.String("env", "", "path to .env.local (auto-discovered if empty)")
	cacheDir := flag.String("cache-dir", filepath.Join("data", "eval_cache"), "local directory for cached embeddings")
	live := flag.Bool("live", false, "call live Qwen3-Embedding-4B API for B4 (2560d and 1024d)")
	legacyJSON := flag.String("legacy-json", "", "optional path to anonymized legacy_mem0_export.json for real-data sanity check")
	verbose := flag.Bool("verbose", false, "print per-query misses and noise false positives for B3 and B4")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if *live {
		if err := loadEnv(*envPath); err != nil {
			fmt.Fprintf(os.Stderr, "load env: %v\n", err)
			os.Exit(1)
		}
	}

	ds := eval.BuildSyntheticDataset()
	tmpDB := filepath.Join(os.TempDir(), fmt.Sprintf("omni_memeval_%d.db", time.Now().UnixNano()))
	defer os.Remove(tmpDB)
	defer os.Remove(tmpDB + "-wal")
	defer os.Remove(tmpDB + "-shm")

	eng, err := eval.NewEngine(ctx, tmpDB, ds)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init engine: %v\n", err)
		os.Exit(1)
	}
	defer eng.Close()

	fmt.Printf("Dataset: %d memories (%d distractors), %d assoc edges, %d queries\n\n",
		len(ds.Memories), countDistractors(ds.Memories), len(ds.Edges), len(ds.Queries))

	// 1. Offline stages B0..B3
	fmt.Println("=== MAIN ABLATION (B0..B4) ===")
	fmt.Printf("%-14s | %8s | %6s | %7s | %6s | %6s | %7s | %8s | %8s | %6s | %6s | %6s | %6s\n",
		"Stage", "Recall@4", "MRR", "TempAcc", "Leak", "Noise", "DistRt", "p50", "p95", "Direct", "Assoc", "Nick", "TempR4")
	fmt.Println(strings.Repeat("-", 128))

	for _, st := range []eval.Stage{eval.StageB0, eval.StageB1, eval.StageB2, eval.StageB3} {
		cfg := eval.DefaultStageConfig(st)
		m, _, err := eng.EvaluateStage(ctx, ds, nil, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "eval %s: %v\n", st, err)
			os.Exit(1)
		}
		printMetricRow(string(st), m)
	}

	var idx2560, idx1024 *eval.VectorIndex
	var qVecs2560, qVecs1024 map[string][]float32

	if *live {
		baseURL := os.Getenv("EMBEDDING_API_BASE")
		apiKey := os.Getenv("EMBEDDING_API_KEY")
		model := os.Getenv("EMBEDDING_MODEL")
		if model == "" {
			model = "Qwen/Qwen3-Embedding-4B"
		}

		var err error
		idx2560, qVecs2560, err = buildLiveVectors(ctx, ds, baseURL, apiKey, model, 2560, *cacheDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "embed 2560d: %v\n", err)
			os.Exit(1)
		}
		idx1024, qVecs1024, err = buildLiveVectors(ctx, ds, baseURL, apiKey, model, 1024, *cacheDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "embed 1024d: %v\n", err)
			os.Exit(1)
		}

		// Print flat Mem0-style pure-vector baseline (2560d) for reference
		mVecOnly := evalPureVectorBaseline(ds, idx2560, qVecs2560, 4)
		printMetricRow("V0-PureVec2560", mVecOnly)

		// B4-2560 and B4-1024
		eng.SetVectors(idx2560)
		cfg2560 := eval.DefaultStageConfig(eval.StageB4)
		m2560, out2560, err := eng.EvaluateStage(ctx, ds, qVecs2560, cfg2560)
		if err != nil {
			fmt.Fprintf(os.Stderr, "eval B4-2560: %v\n", err)
			os.Exit(1)
		}
		printMetricRow("B4-2560", m2560)

		eng.SetVectors(idx1024)
		cfg1024 := eval.DefaultStageConfig(eval.StageB4)
		m1024, out1024, err := eng.EvaluateStage(ctx, ds, qVecs1024, cfg1024)
		if err != nil {
			fmt.Fprintf(os.Stderr, "eval B4-1024: %v\n", err)
			os.Exit(1)
		}
		printMetricRow("B4-1024", m1024)

		// Compare hops=0 on B4-1024 as well (KISS check: anchors+vectors without graph spreading)
		cfg1024NoGraph := eval.DefaultStageConfig(eval.StageB4)
		cfg1024NoGraph.Hops = 0
		m1024NoGraph, out1024NG, err := eng.EvaluateStage(ctx, ds, qVecs1024, cfg1024NoGraph)
		if err != nil {
			fmt.Fprintf(os.Stderr, "eval B4-1024-h0: %v\n", err)
			os.Exit(1)
		}
		printMetricRow("B4-1024-h0", m1024NoGraph)

		if *verbose {
			fmt.Println("\n=== VERBOSE DIAGNOSTICS (B4-2560) ===")
			printDiagnostics(out2560)
			fmt.Println("\n=== VERBOSE DIAGNOSTICS (B4-1024) ===")
			printDiagnostics(out1024)
			fmt.Println("\n=== VERBOSE DIAGNOSTICS (B4-1024-h0) ===")
			printDiagnostics(out1024NG)
		}

		// Cosine similarity distribution diagnostics (positive vs noise queries)
		fmt.Println("\n=== QWEN3-EMBEDDING-4B SIMILARITY DISTRIBUTION (2560d vs 1024d) ===")
		printSimDiagnostics("2560d", ds, idx2560, qVecs2560)
		printSimDiagnostics("1024d", ds, idx1024, qVecs1024)

		// Sub-ablation: MinVectorSim & SurfaceThreshold sweep on B4-2560 and B4-1024
		fmt.Println("\n=== B4 PARAMETER SWEEP (2560d) ===")
		eng.SetVectors(idx2560)
		for _, minSim := range []float32{0.42, 0.46, 0.50, 0.54, 0.58} {
			for _, thr := range []float64{0.35, 0.42, 0.48, 0.54} {
				cfg := eval.DefaultStageConfig(eval.StageB4)
				cfg.MinVectorSim = minSim
				cfg.SurfaceThreshold = thr
				m, _, err := eng.EvaluateStage(ctx, ds, qVecs2560, cfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "sweep: %v\n", err)
					os.Exit(1)
				}
				label := fmt.Sprintf("s=%.2f,t=%.2f", minSim, thr)
				printMetricRow(label, m)
			}
		}

		fmt.Println("\n=== B4 PARAMETER SWEEP (1024d) ===")
		eng.SetVectors(idx1024)
		for _, minSim := range []float32{0.46, 0.50, 0.54} {
			for _, thr := range []float64{0.42, 0.48, 0.54} {
				cfg := eval.DefaultStageConfig(eval.StageB4)
				cfg.MinVectorSim = minSim
				cfg.SurfaceThreshold = thr
				m, _, err := eng.EvaluateStage(ctx, ds, qVecs1024, cfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "sweep 1024: %v\n", err)
					os.Exit(1)
				}
				label := fmt.Sprintf("1024 s=%.2f,t=%.2f", minSim, thr)
				printMetricRow(label, m)
			}
		}
	}

	// 2. Mechanism sub-ablations: Decay model, Anti-Matthew, Spreading Hops
	fmt.Println("\n=== SUB-ABLATIONS (DECAY / ANTI-MATTHEW / SPREADING HOPS) ===")
	// Exponential vs Power-Law on B2 & B3
	for _, st := range []eval.Stage{eval.StageB2, eval.StageB3} {
		for _, decay := range []eval.DecayModel{eval.DecayExponential, eval.DecayPowerLaw} {
			cfg := eval.DefaultStageConfig(st)
			cfg.Decay = decay
			m, _, err := eng.EvaluateStage(ctx, ds, nil, cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "decay sub: %v\n", err)
				os.Exit(1)
			}
			printMetricRow(fmt.Sprintf("%s-%s", st, decay), m)
		}
	}

	// Anti-Matthew vs Naive reinforcement on B1 & B3
	for _, naive := range []bool{false, true} {
		cfg := eval.DefaultStageConfig(eval.StageB1)
		cfg.NaiveReinforcement = naive
		m, _, err := eng.EvaluateStage(ctx, ds, nil, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "matthew sub: %v\n", err)
			os.Exit(1)
		}
		name := "B1-AntiMatthew"
		if naive {
			name = "B1-NaiveCount"
		}
		printMetricRow(name, m)
	}

	// Spreading hops: 0 (entity anchors only) vs 1 vs 2 on B3 and B4-2560
	for _, hops := range []int{0, 1, 2} {
		cfg := eval.DefaultStageConfig(eval.StageB3)
		cfg.Hops = hops
		m, _, err := eng.EvaluateStage(ctx, ds, nil, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hops B3: %v\n", err)
			os.Exit(1)
		}
		printMetricRow(fmt.Sprintf("B3-hops=%d", hops), m)
	}
	if *live && idx2560 != nil {
		eng.SetVectors(idx2560)
		for _, hops := range []int{0, 1, 2} {
			cfg := eval.DefaultStageConfig(eval.StageB4)
			cfg.Hops = hops
			cfg.MinVectorSim = 0.50
			cfg.SurfaceThreshold = 0.48
			m, _, err := eng.EvaluateStage(ctx, ds, qVecs2560, cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "hops B4: %v\n", err)
				os.Exit(1)
			}
			printMetricRow(fmt.Sprintf("B4-hops=%d", hops), m)
		}
	}

	// 3. Pure-Go float32 vector scan latency benchmark at 1k / 5k / 10k
	fmt.Println("\n=== PURE-GO FLOAT32 VECTOR SCAN BENCHMARK (1k / 5k / 10k) ===")
	fmt.Printf("%-6s | %-6s | %-7s | %-7s | %-9s | %-9s | %-9s | %-9s\n",
		"Dim", "Count", "Workers", "RAM(MB)", "p50", "p95", "p99", "Mean")
	fmt.Println(strings.Repeat("-", 80))
	benches := eval.BenchmarkVectorScan([]int{1024, 2560}, []int{1000, 5000, 10000}, []int{1, 4}, 60)
	for _, b := range benches {
		fmt.Printf("%-6d | %-6d | %-7d | %7.2f | %9s | %9s | %9s | %9s\n",
			b.Dim, b.Count, b.Workers, b.MemMB,
			b.P50.Round(time.Microsecond), b.P95.Round(time.Microsecond), b.P99.Round(time.Microsecond), b.Mean.Round(time.Microsecond))
	}

	// 4. Optional real legacy snapshot sanity check
	if *legacyJSON != "" {
		if err := runLegacySanityCheck(ctx, *legacyJSON, *live, *cacheDir); err != nil {
			fmt.Fprintf(os.Stderr, "legacy sanity check: %v\n", err)
			os.Exit(1)
		}
	}
}

func loadEnv(explicitPath string) error {
	if explicitPath != "" {
		return envload.LoadFile(explicitPath)
	}
	_, err := envload.LoadRepoEnv("")
	return err
}

func countDistractors(mems []eval.Memory) int {
	n := 0
	for _, m := range mems {
		if m.IsDistractor {
			n++
		}
	}
	return n
}

func buildLiveVectors(ctx context.Context, ds eval.Dataset, baseURL, apiKey, model string, dim int, cacheDir string) (*eval.VectorIndex, map[string][]float32, error) {
	reqDim := dim
	if dim == 2560 {
		// Native 2560d default omits dimensions field if preferred, or sends 2560 explicitly.
		reqDim = 0
	}
	raw, err := eval.NewOpenAIEmbedder(eval.OpenAIEmbedderConfig{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		Dimensions: reqDim,
		BatchSize:  32,
	})
	if err != nil {
		return nil, nil, err
	}
	cached, err := eval.NewCachedEmbedder(raw, cacheDir, model, dim)
	if err != nil {
		return nil, nil, err
	}

	memTexts := make([]string, len(ds.Memories))
	for i, m := range ds.Memories {
		memTexts[i] = m.Text
	}
	t0 := time.Now()
	memVecs, err := cached.Embed(ctx, memTexts)
	if err != nil {
		return nil, nil, fmt.Errorf("embed memories: %w", err)
	}
	qTexts := make([]string, len(ds.Queries))
	for i, q := range ds.Queries {
		qTexts[i] = q.Text
	}
	qVecList, err := cached.Embed(ctx, qTexts)
	if err != nil {
		return nil, nil, fmt.Errorf("embed queries: %w", err)
	}
	fmt.Printf("[live] Loaded %d memory + %d query vectors (dim=%d) in %s\n",
		len(memVecs), len(qVecList), len(memVecs[0]), time.Since(t0).Round(time.Millisecond))

	idx := eval.NewVectorIndex(len(memVecs[0]))
	for i, m := range ds.Memories {
		if err := idx.Add(m.ID, memVecs[i]); err != nil {
			return nil, nil, err
		}
	}
	qMap := make(map[string][]float32, len(ds.Queries))
	for i, q := range ds.Queries {
		qMap[q.ID] = qVecList[i]
	}
	return idx, qMap, nil
}

func evalPureVectorBaseline(ds eval.Dataset, idx *eval.VectorIndex, qVecs map[string][]float32, topK int) eval.Metrics {
	outcomes := make([]eval.QueryOutcome, len(ds.Queries))
	for i, q := range ds.Queries {
		t0 := time.Now()
		vh := idx.TopK(qVecs[q.ID], topK, -1, 1)
		dt := time.Since(t0)
		hits := make([]eval.Hit, len(vh))
		for j, h := range vh {
			hits[j] = eval.Hit{MemoryID: h.ID, Score: float64(h.Similarity), Rank: j + 1}
		}
		outcomes[i] = eval.QueryOutcome{Query: q, Hits: hits, Latency: dt}
	}
	return eval.Evaluate(outcomes, topK)
}

func printSimDiagnostics(label string, ds eval.Dataset, idx *eval.VectorIndex, qVecs map[string][]float32) {
	var posTop1, noiseTop1 []float64
	for _, q := range ds.Queries {
		vh := idx.TopK(qVecs[q.ID], 1, -1, 1)
		if len(vh) == 0 {
			continue
		}
		if len(q.ExpectedIDs) == 0 {
			noiseTop1 = append(noiseTop1, float64(vh[0].Similarity))
		} else {
			posTop1 = append(posTop1, float64(vh[0].Similarity))
		}
	}
	slices.Sort(posTop1)
	slices.Sort(noiseTop1)
	fmt.Printf("  %s: positive top-1 sim min=%.3f p50=%.3f max=%.3f | noise top-1 sim min=%.3f p50=%.3f max=%.3f\n",
		label,
		posTop1[0], posTop1[len(posTop1)/2], posTop1[len(posTop1)-1],
		noiseTop1[0], noiseTop1[len(noiseTop1)/2], noiseTop1[len(noiseTop1)-1],
	)
}

func printMetricRow(name string, m eval.Metrics) {
	fmt.Printf("%-14s | %8.3f | %6.3f | %7.3f | %6.3f | %6.3f | %7.3f | %8s | %8s | %6.3f | %6.3f | %6.3f | %6.3f\n",
		name,
		m.RecallAt4,
		m.MRR,
		m.TemporalAccuracy,
		m.SupersededLeak,
		m.NoiseRate,
		m.DistractorRate,
		m.LatencyP50.Round(time.Microsecond),
		m.LatencyP95.Round(time.Microsecond),
		m.ByCategory[eval.CategoryDirect].RecallAt4,
		m.ByCategory[eval.CategoryAssociative].RecallAt4,
		m.ByCategory[eval.CategoryNicknameDrift].RecallAt4,
		m.ByCategory[eval.CategoryTemporal].RecallAt4,
	)
}

type legacyRecord struct {
	ID         string    `json:"id"`
	Scope      string    `json:"scope"`
	AnonText   string    `json:"anon_text"`
	CreatedAt  string    `json:"created_at"`
	UpdatedAt  string    `json:"updated_at"`
	HasRelTime bool      `json:"has_rel_time"`
	Vector     []float32 `json:"vector"`
}

func runLegacySanityCheck(ctx context.Context, path string, live bool, cacheDir string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var records []legacyRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	dim := len(records[0].Vector)
	idx := eval.NewVectorIndex(dim)
	for _, r := range records {
		if err := idx.Add(r.ID, r.Vector); err != nil {
			return err
		}
	}

	// Measure background pairwise cosine similarity across all 354 real mem0 vectors.
	sims := make([]float64, 0, len(records)*(len(records)-1)/2)
	var sum float64
	for i := range len(records) {
		vi := eval.NormalizeL2(records[i].Vector)
		for j := i + 1; j < len(records); j++ {
			vj := eval.NormalizeL2(records[j].Vector)
			s := float64(eval.DotProduct(vi, vj))
			sims = append(sims, s)
			sum += s
		}
	}
	slices.Sort(sims)
	mean := sum / float64(len(sims))
	fmt.Printf("\n=== REAL LEGACY SNAPSHOT SANITY CHECK (%d points, dim=%d) ===\n", len(records), dim)
	fmt.Printf("Pairwise cosine distribution: mean=%.4f, p50=%.4f, p90=%.4f, p95=%.4f, p99=%.4f, max=%.4f\n",
		mean,
		sims[len(sims)/2],
		sims[int(float64(len(sims))*0.90)],
		sims[int(float64(len(sims))*0.95)],
		sims[int(float64(len(sims))*0.99)],
		sims[len(sims)-1],
	)

	// Measure duplicate slot occupancy in legacy pure-vector top-4 vs deduped top-4 (MMR/cosine>=0.85 filter)
	// using 5 anonymized real themes embedded live if --live is set.
	if !live {
		return nil
	}
	rawEmb, err := eval.NewOpenAIEmbedder(eval.OpenAIEmbedderConfig{
		BaseURL:   os.Getenv("EMBEDDING_API_BASE"),
		APIKey:    os.Getenv("EMBEDDING_API_KEY"),
		Model:     "Qwen/Qwen3-Embedding-4B",
		BatchSize: 32,
	})
	if err != nil {
		return err
	}
	cached, err := eval.NewCachedEmbedder(rawEmb, cacheDir, "Qwen/Qwen3-Embedding-4B", 2560)
	if err != nil {
		return err
	}

	probes := []struct {
		label string
		query string
	}{
		{"night_shift", "User_4最近是不是在值夜班，几点下班？"},
		{"gpu_status", "User_4现在电脑有没有游戏显卡？"},
		{"meme_pref", "User_1对黄色恐龙表情包什么态度？"},
		{"bot_draw", "群友之前是不是提过让Bot接入画画或工具功能？"},
		{"casual_noise", "哈哈哈哈今天外面刮大风好冷不想出门"},
	}
	qTexts := make([]string, len(probes))
	for i, p := range probes {
		qTexts[i] = p.query
	}
	qVecs, err := cached.Embed(ctx, qTexts)
	if err != nil {
		return err
	}

	byID := make(map[string]legacyRecord, len(records))
	for _, r := range records {
		byID[r.ID] = r
	}

	for i, p := range probes {
		top4 := idx.TopK(qVecs[i], 4, -1, 1)
		// Count redundant pairs (cosine >= 0.85) inside legacy top-4
		dupPairs := 0
		for a := range len(top4) {
			va := eval.NormalizeL2(byID[top4[a].ID].Vector)
			for b := a + 1; b < len(top4); b++ {
				vb := eval.NormalizeL2(byID[top4[b].ID].Vector)
				if eval.DotProduct(va, vb) >= 0.85 {
					dupPairs++
				}
			}
		}
		// Compare against deduped + thresholded top-4 (minSim=0.50, pairwise diversity <0.85)
		candidates := idx.TopK(qVecs[i], 16, 0.50, 1)
		var deduped []eval.VectorHit
		for _, c := range candidates {
			vc := eval.NormalizeL2(byID[c.ID].Vector)
			redundant := false
			for _, kept := range deduped {
				vk := eval.NormalizeL2(byID[kept.ID].Vector)
				if eval.DotProduct(vc, vk) >= 0.85 {
					redundant = true
					break
				}
			}
			if !redundant {
				deduped = append(deduped, c)
				if len(deduped) == 4 {
					break
				}
			}
		}
		fmt.Printf("  Probe %-13s | legacy_top4: top1=%.3f dup_pairs=%d | filtered_dedup_top4: count=%d top1=%.3f\n",
			p.label, firstSim(top4), dupPairs, len(deduped), firstSim(deduped))
	}
	return nil
}

func firstSim(hits []eval.VectorHit) float32 {
	if len(hits) == 0 {
		return 0
	}
	return float32(math.Round(float64(hits[0].Similarity)*1000) / 1000)
}

func printDiagnostics(outcomes []eval.QueryOutcome) {
	for _, oc := range outcomes {
		if len(oc.Query.ExpectedIDs) == 0 {
			if len(oc.Hits) > 0 {
				fmt.Printf("  [NOISE FP] %s (%s): top1=%s (%.3f) total=%d\n",
					oc.Query.ID, oc.Query.Text, oc.Hits[0].MemoryID, oc.Hits[0].Score, len(oc.Hits))
			}
			continue
		}
		want := oc.Query.ExpectedIDs[0]
		rank := 0
		for i, h := range oc.Hits {
			if h.MemoryID == want {
				rank = i + 1
				break
			}
		}
		if rank == 0 || (oc.Query.Category == eval.CategoryTemporal && rank > 1) {
			top1 := "<none>"
			var top1Score float64
			if len(oc.Hits) > 0 {
				top1 = oc.Hits[0].MemoryID
				top1Score = oc.Hits[0].Score
			}
			fmt.Printf("  [MISS/RANK=%d] %s (%s) want=%s | top1=%s (%.3f)\n",
				rank, oc.Query.ID, oc.Query.Category, want, top1, top1Score)
		}
	}
}
