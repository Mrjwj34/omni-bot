package eval

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

type VectorHit struct {
	ID         string
	Similarity float32
}

// VectorIndex stores L2-normalized float32 embeddings in one contiguous slice for cache-friendly dot scans.
type VectorIndex struct {
	dim     int
	ids     []string
	idToIdx map[string]int
	data    []float32
}

func NewVectorIndex(dim int) *VectorIndex {
	return &VectorIndex{
		dim:     dim,
		idToIdx: make(map[string]int),
	}
}

func (v *VectorIndex) Dim() int { return v.dim }
func (v *VectorIndex) Len() int { return len(v.ids) }

func (v *VectorIndex) Add(id string, vec []float32) error {
	if len(vec) != v.dim {
		return fmt.Errorf("vector dim mismatch for %q: got %d, want %d", id, len(vec), v.dim)
	}
	norm := NormalizeL2(vec)
	if idx, exists := v.idToIdx[id]; exists {
		copy(v.data[idx*v.dim:(idx+1)*v.dim], norm)
		return nil
	}
	v.idToIdx[id] = len(v.ids)
	v.ids = append(v.ids, id)
	v.data = append(v.data, norm...)
	return nil
}

// TopK scans all vectors via dot product (equivalent to cosine similarity on L2-normalized rows).
// Pass workers > 1 to split the scan across goroutines.
func (v *VectorIndex) TopK(query []float32, k int, minSim float32, workers int) []VectorHit {
	n := len(v.ids)
	if n == 0 || k <= 0 || len(query) != v.dim {
		return nil
	}
	q := NormalizeL2(query)

	if workers <= 1 || n < 512 {
		return v.scanRange(q, 0, n, k, minSim)
	}
	if workers > n {
		workers = n
	}

	partials := make([][]VectorHit, workers)
	var wg sync.WaitGroup
	chunk := (n + workers - 1) / workers
	for w := range workers {
		start := w * chunk
		end := min(start+chunk, n)
		if start >= end {
			break
		}
		wg.Add(1)
		go func(slot, s, e int) {
			defer wg.Done()
			partials[slot] = v.scanRange(q, s, e, k, minSim)
		}(w, start, end)
	}
	wg.Wait()

	merged := make([]VectorHit, 0, k*workers)
	for _, p := range partials {
		merged = append(merged, p...)
	}
	slices.SortFunc(merged, compareVectorHitDesc)
	if len(merged) > k {
		merged = merged[:k]
	}
	return merged
}

func (v *VectorIndex) scanRange(q []float32, start, end, k int, minSim float32) []VectorHit {
	dim := v.dim
	data := v.data
	top := make([]VectorHit, 0, min(k+1, end-start))
	threshold := minSim

	for i := start; i < end; i++ {
		sim := DotProduct(q, data[i*dim:(i+1)*dim])
		if sim < threshold {
			continue
		}
		hit := VectorHit{ID: v.ids[i], Similarity: sim}
		if len(top) < k {
			top = append(top, hit)
			if len(top) == k {
				slices.SortFunc(top, compareVectorHitDesc)
				if top[k-1].Similarity > threshold {
					threshold = top[k-1].Similarity
				}
			}
		} else {
			// Insert into sorted descending top-k slice and raise threshold.
			pos, _ := slices.BinarySearchFunc(top, hit, compareVectorHitDesc)
			if pos < k {
				copy(top[pos+1:], top[pos:k-1])
				top[pos] = hit
				if top[k-1].Similarity > threshold {
					threshold = top[k-1].Similarity
				}
			}
		}
	}
	slices.SortFunc(top, compareVectorHitDesc)
	return top
}

func compareVectorHitDesc(a, b VectorHit) int {
	return cmp.Or(cmp.Compare(b.Similarity, a.Similarity), cmp.Compare(a.ID, b.ID))
}

// NormalizeL2 returns a new L2-normalized copy of vec (or zero vector if norm is 0).
func NormalizeL2(vec []float32) []float32 {
	out := make([]float32, len(vec))
	var sum float64
	for _, x := range vec {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return out
	}
	inv := float32(1.0 / math.Sqrt(sum))
	for i, x := range vec {
		out[i] = x * inv
	}
	return out
}

// DotProduct computes the inner product of equal-length slices with 4-way unrolling.
func DotProduct(a, b []float32) float32 {
	n := len(a)
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+3 < n; i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	sum := (s0 + s1) + (s2 + s3)
	for ; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}

type VectorScanBench struct {
	Dim     int
	Count   int
	Workers int
	MemMB   float64
	P50     time.Duration
	P95     time.Duration
	P99     time.Duration
	Mean    time.Duration
}

// BenchmarkVectorScan measures pure-Go float32 flat-slice Top-K scan latency across sizes and dimensions.
func BenchmarkVectorScan(dims []int, counts []int, workerCounts []int, queries int) []VectorScanBench {
	if queries <= 0 {
		queries = 50
	}
	rng := rand.New(rand.NewPCG(42, 2026))
	var results []VectorScanBench

	for _, dim := range dims {
		for _, count := range counts {
			idx := NewVectorIndex(dim)
			idx.ids = make([]string, count)
			idx.data = make([]float32, count*dim)
			buf := make([]float32, dim)
			for i := range count {
				idx.ids[i] = fmt.Sprintf("v_%05d", i)
				idx.idToIdx[idx.ids[i]] = i
				for d := range dim {
					buf[d] = rng.Float32()*2 - 1
				}
				copy(idx.data[i*dim:(i+1)*dim], NormalizeL2(buf))
			}

			qVecs := make([][]float32, queries)
			for q := range queries {
				qv := make([]float32, dim)
				for d := range dim {
					qv[d] = rng.Float32()*2 - 1
				}
				qVecs[q] = NormalizeL2(qv)
			}

			memMB := float64(count*dim*4) / (1024 * 1024)
			for _, workers := range workerCounts {
				// Warm up once before timing.
				_ = idx.TopK(qVecs[0], 10, -1, workers)

				samples := make([]time.Duration, queries)
				var total time.Duration
				for q := range queries {
					t0 := time.Now()
					_ = idx.TopK(qVecs[q], 10, -1, workers)
					dt := time.Since(t0)
					samples[q] = dt
					total += dt
				}
				results = append(results, VectorScanBench{
					Dim:     dim,
					Count:   count,
					Workers: workers,
					MemMB:   memMB,
					P50:     Percentile(samples, 50),
					P95:     Percentile(samples, 95),
					P99:     Percentile(samples, 99),
					Mean:    total / time.Duration(queries),
				})
			}
		}
	}
	return results
}
