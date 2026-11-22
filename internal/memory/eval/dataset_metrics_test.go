package eval

import (
	"math"
	"testing"
	"time"
)

func TestBuildSyntheticDatasetInvariants(t *testing.T) {
	ds := BuildSyntheticDataset()

	if len(ds.Memories) < 600 {
		t.Fatalf("expected >= 600 memories including distractors, got %d", len(ds.Memories))
	}
	if len(ds.Queries) < 40 {
		t.Fatalf("expected >= 40 labeled queries, got %d", len(ds.Queries))
	}

	byID := make(map[string]Memory, len(ds.Memories))
	var distractorCount, supersededCount int
	for _, m := range ds.Memories {
		if m.ID == "" || m.Text == "" {
			t.Fatalf("memory with empty id or text: %+v", m)
		}
		if _, dup := byID[m.ID]; dup {
			t.Fatalf("duplicate memory id %q", m.ID)
		}
		byID[m.ID] = m
		if m.IsDistractor {
			distractorCount++
		}
		if m.SupersededBy != "" {
			supersededCount++
		}
	}

	if distractorCount < 500 {
		t.Fatalf("expected >= 500 high-distractor noise entries, got %d", distractorCount)
	}
	if supersededCount < 10 {
		t.Fatalf("expected >= 10 superseded temporal states, got %d", supersededCount)
	}

	// Verify temporal superseded_by chains point to valid newer records and have no cycles.
	for _, m := range ds.Memories {
		if m.SupersededBy == "" {
			continue
		}
		seen := map[string]bool{m.ID: true}
		cur := m
		for cur.SupersededBy != "" {
			next, ok := byID[cur.SupersededBy]
			if !ok {
				t.Fatalf("memory %q superseded_by unknown id %q", cur.ID, cur.SupersededBy)
			}
			if seen[next.ID] {
				t.Fatalf("cycle detected in superseded_by chain at %q", next.ID)
			}
			if next.TEvent <= cur.TEvent {
				t.Fatalf("superseding memory %q (t=%d) not newer than %q (t=%d)", next.ID, next.TEvent, cur.ID, cur.TEvent)
			}
			seen[next.ID] = true
			cur = next
		}
	}

	// Verify association edges respect canonical a < b ordering and valid endpoints.
	if len(ds.Edges) == 0 {
		t.Fatal("expected non-empty memory_assoc edges")
	}
	for _, e := range ds.Edges {
		if !(e.A < e.B) {
			t.Fatalf("assoc edge violates a < b invariant: %q >= %q", e.A, e.B)
		}
		if _, ok := byID[e.A]; !ok {
			t.Fatalf("assoc edge references missing memory %q", e.A)
		}
		if _, ok := byID[e.B]; !ok {
			t.Fatalf("assoc edge references missing memory %q", e.B)
		}
		if e.Weight <= 0 || e.Weight > 1 {
			t.Fatalf("assoc edge (%s,%s) invalid weight %v", e.A, e.B, e.Weight)
		}
	}

	// Verify all 5 query categories are populated and every labeled ID exists.
	catCounts := make(map[QueryCategory]int)
	for _, q := range ds.Queries {
		catCounts[q.Category]++
		for _, id := range q.ExpectedIDs {
			m, ok := byID[id]
			if !ok {
				t.Fatalf("query %q expects unknown memory %q", q.ID, id)
			}
			if m.SupersededBy != "" {
				t.Fatalf("query %q expects superseded memory %q", q.ID, id)
			}
		}
		for _, id := range q.SupersededIDs {
			m, ok := byID[id]
			if !ok {
				t.Fatalf("query %q lists unknown superseded memory %q", q.ID, id)
			}
			if m.SupersededBy == "" {
				t.Fatalf("query %q lists non-superseded memory %q in SupersededIDs", q.ID, id)
			}
		}
		if q.Category == CategoryNoiseRejection && len(q.ExpectedIDs) != 0 {
			t.Fatalf("noise rejection query %q must have empty ExpectedIDs", q.ID)
		}
		if q.Category == CategoryTemporal && (len(q.ExpectedIDs) == 0 || len(q.SupersededIDs) == 0) {
			t.Fatalf("temporal query %q must specify both ExpectedIDs and SupersededIDs", q.ID)
		}
	}
	for _, cat := range []QueryCategory{
		CategoryDirect,
		CategoryAssociative,
		CategoryNicknameDrift,
		CategoryTemporal,
		CategoryNoiseRejection,
	} {
		if catCounts[cat] == 0 {
			t.Fatalf("missing queries for category %q", cat)
		}
	}

	// Determinism check.
	ds2 := BuildSyntheticDataset()
	if len(ds.Memories) != len(ds2.Memories) || ds.Memories[len(ds.Memories)-1].Text != ds2.Memories[len(ds2.Memories)-1].Text {
		t.Fatal("BuildSyntheticDataset must be deterministic")
	}
}

func TestEvaluateMetrics(t *testing.T) {
	tests := []struct {
		name             string
		outcomes         []QueryOutcome
		wantRecall       float64
		wantMRR          float64
		wantTemporalAcc  float64
		wantSupersededLk float64
		wantNoiseRate    float64
		wantP50          time.Duration
		wantP95          time.Duration
	}{
		{
			name: "perfect retrieval and noise rejection",
			outcomes: []QueryOutcome{
				{
					Query:   Query{ID: "q1", Category: CategoryDirect, ExpectedIDs: []string{"m1"}},
					Hits:    []Hit{{MemoryID: "m1", Score: 1.0, Rank: 1}},
					Latency: 1 * time.Millisecond,
				},
				{
					Query:   Query{ID: "q2", Category: CategoryTemporal, ExpectedIDs: []string{"m_new"}, SupersededIDs: []string{"m_old"}},
					Hits:    []Hit{{MemoryID: "m_new", Score: 0.9, Rank: 1}},
					Latency: 2 * time.Millisecond,
				},
				{
					Query:   Query{ID: "q3", Category: CategoryNoiseRejection},
					Hits:    nil,
					Latency: 3 * time.Millisecond,
				},
			},
			wantRecall:       1.0,
			wantMRR:          1.0,
			wantTemporalAcc:  1.0,
			wantSupersededLk: 0.0,
			wantNoiseRate:    0.0,
			wantP50:          2 * time.Millisecond,
			wantP95:          3 * time.Millisecond,
		},
		{
			name: "superseded beats latest and noise leaks",
			outcomes: []QueryOutcome{
				{
					Query: Query{ID: "q1", Category: CategoryDirect, ExpectedIDs: []string{"m1", "m2"}},
					Hits: []Hit{
						{MemoryID: "d1", Score: 0.9, Rank: 1},
						{MemoryID: "m1", Score: 0.8, Rank: 2},
						{MemoryID: "d2", Score: 0.7, Rank: 3},
						{MemoryID: "d3", Score: 0.6, Rank: 4},
						{MemoryID: "m2", Score: 0.5, Rank: 5}, // outside top-4
					},
					Latency: 10 * time.Millisecond,
				},
				{
					Query: Query{ID: "q2", Category: CategoryTemporal, ExpectedIDs: []string{"m_new"}, SupersededIDs: []string{"m_old"}},
					Hits: []Hit{
						{MemoryID: "m_old", Score: 0.95, Rank: 1},
						{MemoryID: "m_new", Score: 0.80, Rank: 2},
					},
					Latency: 20 * time.Millisecond,
				},
				{
					Query:   Query{ID: "q3", Category: CategoryNoiseRejection},
					Hits:    []Hit{{MemoryID: "d9", Score: 0.4, Rank: 1}},
					Latency: 30 * time.Millisecond,
				},
				{
					Query:   Query{ID: "q4", Category: CategoryNoiseRejection},
					Hits:    nil,
					Latency: 40 * time.Millisecond,
				},
			},
			// q1 recall=0.5, mrr=0.5; q2 recall=1.0, mrr=0.5 => mean recall=0.75, mrr=0.5
			wantRecall:       0.75,
			wantMRR:          0.5,
			wantTemporalAcc:  0.0,
			wantSupersededLk: 1.0,
			wantNoiseRate:    0.5,
			wantP50:          20 * time.Millisecond,
			wantP95:          40 * time.Millisecond,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.outcomes, 4)
			assertNear(t, "RecallAt4", got.RecallAt4, tc.wantRecall)
			assertNear(t, "MRR", got.MRR, tc.wantMRR)
			assertNear(t, "TemporalAccuracy", got.TemporalAccuracy, tc.wantTemporalAcc)
			assertNear(t, "SupersededLeak", got.SupersededLeak, tc.wantSupersededLk)
			assertNear(t, "NoiseRate", got.NoiseRate, tc.wantNoiseRate)
			if got.LatencyP50 != tc.wantP50 {
				t.Errorf("LatencyP50 = %v, want %v", got.LatencyP50, tc.wantP50)
			}
			if got.LatencyP95 != tc.wantP95 {
				t.Errorf("LatencyP95 = %v, want %v", got.LatencyP95, tc.wantP95)
			}
		})
	}
}

func assertNear(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %.6f, want %.6f", label, got, want)
	}
}
