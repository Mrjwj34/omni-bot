package memory

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"sync"

	"omni-bot/internal/store"
)

const (
	DefaultVectorDim     = 1024
	DefaultVectorWorkers = 4
)

// VectorEntry stores hot-path metadata alongside a cached L2-normalized embedding.
type VectorEntry struct {
	ID             string
	RoomID         string
	Kind           string
	TEventMs       int64
	Importance     int
	RecallCount    int
	LastRecalledMs int64
}

// VectorHit pairs a cached memory ID with its cosine similarity to a query vector.
type VectorHit struct {
	ID         string
	RoomID     string
	Similarity float32
}

// VectorCache holds active L2-normalized float32 embeddings in one contiguous slice
// for concurrent pure-Go dot-product top-K scans.
type VectorCache struct {
	mu      sync.RWMutex
	dim     int
	entries []VectorEntry
	idToIdx map[string]int
	data    []float32
}

func NewVectorCache(dim int) *VectorCache {
	if dim <= 0 {
		dim = DefaultVectorDim
	}
	return &VectorCache{
		dim:     dim,
		idToIdx: make(map[string]int),
	}
}

func (c *VectorCache) Dim() int {
	return c.dim
}

func (c *VectorCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Load replaces the cache contents from active SQLite memory vectors.
func (c *VectorCache) Load(vectors []store.MemoryVector) error {
	entries := make([]VectorEntry, 0, len(vectors))
	idToIdx := make(map[string]int, len(vectors))
	data := make([]float32, 0, len(vectors)*c.dim)

	for _, mv := range vectors {
		if len(mv.Embedding) == 0 {
			continue
		}
		if len(mv.Embedding) != c.dim {
			return fmt.Errorf("memory: vector dim mismatch for %q: got %d, want %d", mv.ID, len(mv.Embedding), c.dim)
		}
		norm := NormalizeL2(mv.Embedding)
		entry := VectorEntry{
			ID:             mv.ID,
			RoomID:         mv.RoomID,
			Kind:           mv.Kind,
			TEventMs:       mv.TEventMs,
			Importance:     mv.Importance,
			RecallCount:    mv.RecallCount,
			LastRecalledMs: mv.LastRecalledMs,
		}
		if idx, exists := idToIdx[mv.ID]; exists {
			entries[idx] = entry
			copy(data[idx*c.dim:(idx+1)*c.dim], norm)
			continue
		}
		idToIdx[mv.ID] = len(entries)
		entries = append(entries, entry)
		data = append(data, norm...)
	}

	c.mu.Lock()
	c.entries = entries
	c.idToIdx = idToIdx
	c.data = data
	c.mu.Unlock()
	return nil
}

// ReplaceRoom replaces only entries belonging to roomID while preserving other rooms in the cache.
func (c *VectorCache) ReplaceRoom(roomID string, vectors []store.MemoryVector) error {
	if roomID == "" {
		return c.Load(vectors)
	}
	normed := make([][]float32, 0, len(vectors))
	incoming := make([]VectorEntry, 0, len(vectors))
	for _, mv := range vectors {
		if len(mv.Embedding) == 0 {
			continue
		}
		if len(mv.Embedding) != c.dim {
			return fmt.Errorf("memory: vector dim mismatch for %q: got %d, want %d", mv.ID, len(mv.Embedding), c.dim)
		}
		incoming = append(incoming, VectorEntry{
			ID:             mv.ID,
			RoomID:         mv.RoomID,
			Kind:           mv.Kind,
			TEventMs:       mv.TEventMs,
			Importance:     mv.Importance,
			RecallCount:    mv.RecallCount,
			LastRecalledMs: mv.LastRecalledMs,
		})
		normed = append(normed, NormalizeL2(mv.Embedding))
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	keepEntries := make([]VectorEntry, 0, len(c.entries)+len(incoming))
	keepIdx := make(map[string]int, len(c.entries)+len(incoming))
	keepData := make([]float32, 0, (len(c.entries)+len(incoming))*c.dim)

	for i, e := range c.entries {
		if e.RoomID == roomID {
			continue
		}
		keepIdx[e.ID] = len(keepEntries)
		keepEntries = append(keepEntries, e)
		keepData = append(keepData, c.data[i*c.dim:(i+1)*c.dim]...)
	}
	for i, e := range incoming {
		if idx, exists := keepIdx[e.ID]; exists {
			keepEntries[idx] = e
			copy(keepData[idx*c.dim:(idx+1)*c.dim], normed[i])
			continue
		}
		keepIdx[e.ID] = len(keepEntries)
		keepEntries = append(keepEntries, e)
		keepData = append(keepData, normed[i]...)
	}

	c.entries = keepEntries
	c.idToIdx = keepIdx
	c.data = keepData
	return nil
}

// Upsert inserts or updates an entry and its L2-normalized embedding in the hot cache.
func (c *VectorCache) Upsert(entry VectorEntry, vec []float32) error {
	if entry.ID == "" {
		return fmt.Errorf("memory: vector cache upsert requires non-empty id")
	}
	if len(vec) != c.dim {
		return fmt.Errorf("memory: vector dim mismatch for %q: got %d, want %d", entry.ID, len(vec), c.dim)
	}
	norm := NormalizeL2(vec)

	c.mu.Lock()
	defer c.mu.Unlock()
	if idx, exists := c.idToIdx[entry.ID]; exists {
		c.entries[idx] = entry
		copy(c.data[idx*c.dim:(idx+1)*c.dim], norm)
		return nil
	}
	c.idToIdx[entry.ID] = len(c.entries)
	c.entries = append(c.entries, entry)
	c.data = append(c.data, norm...)
	return nil
}

// UpdateMeta mutates metadata for an existing cached entry without touching its vector.
func (c *VectorCache) UpdateMeta(id string, fn func(*VectorEntry)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, ok := c.idToIdx[id]
	if !ok {
		return false
	}
	fn(&c.entries[idx])
	return true
}

// Delete removes id from the cache in O(1) time via swap-remove.
func (c *VectorCache) Delete(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, ok := c.idToIdx[id]
	if !ok {
		return
	}
	last := len(c.entries) - 1
	if idx != last {
		lastEntry := c.entries[last]
		c.entries[idx] = lastEntry
		c.idToIdx[lastEntry.ID] = idx
		copy(c.data[idx*c.dim:(idx+1)*c.dim], c.data[last*c.dim:(last+1)*c.dim])
	}
	clear(c.data[last*c.dim : (last+1)*c.dim])
	c.entries = c.entries[:last]
	c.data = c.data[:last*c.dim]
	delete(c.idToIdx, id)
}

// Get returns the cached metadata and a copy of the normalized embedding for id.
func (c *VectorCache) Get(id string) (VectorEntry, []float32, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	idx, ok := c.idToIdx[id]
	if !ok {
		return VectorEntry{}, nil, false
	}
	vec := make([]float32, c.dim)
	copy(vec, c.data[idx*c.dim:(idx+1)*c.dim])
	return c.entries[idx], vec, true
}

// ListByRoom returns a snapshot of cached entries matching roomID (or all rooms if roomID is empty).
func (c *VectorCache) ListByRoom(roomID string) []VectorEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]VectorEntry, 0, len(c.entries))
	for _, e := range c.entries {
		if roomID != "" && e.RoomID != roomID {
			continue
		}
		out = append(out, e)
	}
	return out
}

// SnapshotVectors returns a lock-free copy of entries and contiguous normalized vectors for roomID.
func (c *VectorCache) SnapshotVectors(roomID string) ([]VectorEntry, []float32) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := make([]VectorEntry, 0, len(c.entries))
	data := make([]float32, 0, len(c.entries)*c.dim)
	for i, e := range c.entries {
		if roomID != "" && e.RoomID != roomID {
			continue
		}
		entries = append(entries, e)
		data = append(data, c.data[i*c.dim:(i+1)*c.dim]...)
	}
	return entries, data
}

// TopK scans cached vectors matching roomID (or all rooms if empty) using dot product.
// Holding RLock across pure-CPU memory scans avoids copying tens of megabytes while keeping zero I/O under lock.
func (c *VectorCache) TopK(roomID string, query []float32, k int, minSim float32, workers int) []VectorHit {
	if k <= 0 || len(query) != c.dim {
		return nil
	}
	q := NormalizeL2(query)

	c.mu.RLock()
	defer c.mu.RUnlock()

	n := len(c.entries)
	if n == 0 {
		return nil
	}
	if workers <= 1 || n < 512 {
		return c.scanRangeLocked(roomID, q, 0, n, k, minSim)
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
			partials[slot] = c.scanRangeLocked(roomID, q, s, e, k, minSim)
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

func (c *VectorCache) scanRangeLocked(roomID string, q []float32, start, end, k int, minSim float32) []VectorHit {
	dim := c.dim
	data := c.data
	entries := c.entries
	top := make([]VectorHit, 0, min(k+1, end-start))
	threshold := minSim

	for i := start; i < end; i++ {
		if roomID != "" && entries[i].RoomID != roomID {
			continue
		}
		sim := DotProduct(q, data[i*dim:(i+1)*dim])
		if sim < threshold {
			continue
		}
		hit := VectorHit{
			ID:         entries[i].ID,
			RoomID:     entries[i].RoomID,
			Similarity: sim,
		}
		if len(top) < k {
			top = append(top, hit)
			if len(top) == k {
				slices.SortFunc(top, compareVectorHitDesc)
				if top[k-1].Similarity > threshold {
					threshold = top[k-1].Similarity
				}
			}
		} else {
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

// NormalizeL2 returns a unit-length copy of vec (or a zero slice if the norm is zero).
func NormalizeL2(vec []float32) []float32 {
	out := make([]float32, len(vec))
	var sum float64
	for _, x := range vec {
		f := float64(x)
		sum += f * f
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
	n := min(len(a), len(b))
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

// CosineSimilarity computes cosine similarity between two equal-length vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	return DotProduct(NormalizeL2(a), NormalizeL2(b))
}
