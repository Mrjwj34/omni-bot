package meme

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"omni-bot/internal/store"
)

const (
	IngestStatusIngested       = "ingested"
	IngestStatusDuplicateHash  = "duplicate_hash"
	IngestStatusDuplicatePHash = "duplicate_phash"
	IngestStatusBlacklisted    = "blacklisted"
	IngestStatusRejected       = "rejected"
)

var ErrMemeNotFound = errors.New("meme: not found")

// VisionTag holds structured visual tags produced by a vision model for a meme image.
type VisionTag struct {
	OverlayText  string
	Description  string
	Emotions     string
	Scenes       string
	IsMeme       *bool
	RejectReason string
}

// VisionTagger describes and classifies meme images (including 2x2 GIF contact sheets).
type VisionTagger interface {
	TagMeme(ctx context.Context, imageBytes []byte, mimeType string, isAnimated bool, hint string) (VisionTag, error)
}

// Embedder computes dense text embeddings for meme search blobs.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Options configures a meme Vault.
type Options struct {
	Store         *store.Store
	Blobs         *store.BlobStore
	Tagger        VisionTagger
	Embedder      Embedder
	DedupDistance int
	ThumbMaxEdge  int
	Logger        *slog.Logger
}

// IngestResult reports the outcome of a Vault.Ingest call.
type IngestResult struct {
	Meme        store.Meme `json:"meme,omitempty"`
	Status      string     `json:"status"`
	DuplicateOf string     `json:"duplicate_of,omitempty"`
}

// HandItem represents a ranked meme candidate returned for the prompt hand rack.
type HandItem struct {
	Meme         store.Meme `json:"meme"`
	ShortID      string     `json:"short_id"`
	Score        float64    `json:"score"`
	KeywordScore float64    `json:"keyword_score"`
	VectorScore  float64    `json:"vector_score"`
}

// ImpressionPatch carries optional metadata overrides for Vault.UpdateImpression.
type ImpressionPatch struct {
	OverlayText *string
	Description *string
	Emotions    *string
	Scenes      *string
}

// Vault manages meme ingestion, perceptual deduplication, hybrid retrieval, and lifecycle operations.
type Vault struct {
	store         *store.Store
	blobs         *store.BlobStore
	tagger        VisionTagger
	embedder      Embedder
	dedupDistance int
	thumbMaxEdge  int
	logger        *slog.Logger

	mu             sync.RWMutex
	blockedPHashes []uint64
}

func NewVault(opts Options) (*Vault, error) {
	if opts.Store == nil {
		return nil, errors.New("meme: store is required")
	}
	if opts.Blobs == nil {
		return nil, errors.New("meme: blob store is required")
	}
	dedupDist := opts.DedupDistance
	if dedupDist <= 0 {
		dedupDist = DefaultDedupDistance
	}
	thumbEdge := opts.ThumbMaxEdge
	if thumbEdge <= 0 {
		thumbEdge = defaultThumbMaxEdge
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Vault{
		store:         opts.Store,
		blobs:         opts.Blobs,
		tagger:        opts.Tagger,
		embedder:      opts.Embedder,
		dedupDistance: dedupDist,
		thumbMaxEdge:  thumbEdge,
		logger:        logger,
	}, nil
}

// Ingest deduplicates rawBytes by SHA-256 and 64-bit dHash, stores image + thumbnail, tags, embeds, and persists the meme.
func (v *Vault) Ingest(ctx context.Context, rawBytes []byte, ext string, hint string) (IngestResult, error) {
	if len(rawBytes) == 0 {
		return IngestResult{}, errors.New("meme: empty raw image bytes")
	}
	if err := ctx.Err(); err != nil {
		return IngestResult{}, err
	}

	sum := sha256.Sum256(rawBytes)
	hash := hex.EncodeToString(sum[:])

	if seen, ok, err := v.store.GetMemeSeen(ctx, hash); err != nil {
		return IngestResult{}, fmt.Errorf("meme: check seen %q: %w", hash, err)
	} else if ok && isBlockedSeenStatus(seen.Status) {
		return IngestResult{Status: IngestStatusBlacklisted}, nil
	}

	if existing, ok, err := v.store.GetMeme(ctx, hash); err != nil {
		return IngestResult{}, fmt.Errorf("meme: lookup existing %q: %w", hash, err)
	} else if ok {
		if existing.Status == store.MemeStatusDeleted {
			return IngestResult{Status: IngestStatusBlacklisted}, nil
		}
		return IngestResult{
			Meme:        existing,
			Status:      IngestStatusDuplicateHash,
			DuplicateOf: existing.Hash,
		}, nil
	}

	phashU64, phashHex, err := ComputeDHashBytes(rawBytes)
	if err != nil {
		return IngestResult{}, err
	}

	if v.isPHashBlocked(phashU64) {
		_ = v.store.MarkMemeSeen(ctx, store.MemeSeen{
			Hash:   hash,
			PHash:  phashHex,
			Status: store.MemeStatusDeleted,
			Label:  "blocked_phash",
		})
		return IngestResult{Status: IngestStatusBlacklisted}, nil
	}

	active, err := v.store.ListActiveMemes(ctx)
	if err != nil {
		return IngestResult{}, fmt.Errorf("meme: list active for phash dedup: %w", err)
	}
	for _, existing := range active {
		existingPHash, perr := ParsePHash(existing.PHash)
		if perr != nil {
			continue
		}
		if HammingDistance(phashU64, existingPHash) <= v.dedupDistance {
			if err := v.store.MarkMemeSeen(ctx, store.MemeSeen{
				Hash:   hash,
				PHash:  phashHex,
				Status: "duplicate",
				Label:  existing.Hash,
			}); err != nil {
				return IngestResult{}, fmt.Errorf("meme: mark duplicate seen: %w", err)
			}
			return IngestResult{
				Meme:        existing,
				Status:      IngestStatusDuplicatePHash,
				DuplicateOf: existing.Hash,
			}, nil
		}
	}

	visionBytes, visionMIME, isAnimated, err := BuildVisionImage(rawBytes)
	if err != nil {
		return IngestResult{}, err
	}
	thumbBytes, err := RenderThumbnail(rawBytes, v.thumbMaxEdge)
	if err != nil {
		return IngestResult{}, err
	}

	var tag VisionTag
	if v.tagger != nil {
		tag, err = v.tagger.TagMeme(ctx, visionBytes, visionMIME, isAnimated, hint)
		if err != nil {
			return IngestResult{}, fmt.Errorf("meme: vision tag: %w", err)
		}
		if tag.IsMeme != nil && !*tag.IsMeme {
			if err := v.store.MarkMemeSeen(ctx, store.MemeSeen{
				Hash:   hash,
				PHash:  phashHex,
				Status: "rejected",
				Label:  strings.TrimSpace(tag.RejectReason),
			}); err != nil {
				return IngestResult{}, fmt.Errorf("meme: mark rejected seen: %w", err)
			}
			return IngestResult{Status: IngestStatusRejected}, nil
		}
	} else {
		tag.Description = strings.TrimSpace(hint)
	}

	cleanExt := sanitizeExt(ext, rawBytes)
	relPath, _, err := v.blobs.PutMemeFile(ctx, hash+cleanExt, bytes.NewReader(rawBytes))
	if err != nil {
		return IngestResult{}, fmt.Errorf("meme: store raw file: %w", err)
	}
	thumbRelPath, _, err := v.blobs.PutMemeFile(ctx, path.Join("thumbs", hash+".jpg"), bytes.NewReader(thumbBytes))
	if err != nil {
		return IngestResult{}, fmt.Errorf("meme: store thumbnail: %w", err)
	}

	searchBlob := buildSearchBlob(tag.OverlayText, tag.Description, tag.Emotions, tag.Scenes, hint)
	var vec []float32
	if v.embedder != nil && searchBlob != "" {
		vec, err = v.embedder.Embed(ctx, searchBlob)
		if err != nil {
			return IngestResult{}, fmt.Errorf("meme: embed search blob: %w", err)
		}
	}

	nowMs := time.Now().UnixMilli()
	m := store.Meme{
		Hash:         hash,
		RelPath:      relPath,
		ThumbRelPath: thumbRelPath,
		PHash:        phashHex,
		OverlayText:  strings.TrimSpace(tag.OverlayText),
		Description:  strings.TrimSpace(tag.Description),
		Emotions:     strings.TrimSpace(tag.Emotions),
		Scenes:       strings.TrimSpace(tag.Scenes),
		SearchBlob:   searchBlob,
		Embedding:    vec,
		EmbeddingDim: len(vec),
		UseCount:     0,
		Status:       store.MemeStatusActive,
		CreatedAtMs:  nowMs,
		UpdatedAtMs:  nowMs,
	}
	if err := v.store.UpsertMeme(ctx, m); err != nil {
		return IngestResult{}, fmt.Errorf("meme: upsert meme: %w", err)
	}
	if err := v.store.MarkMemeSeen(ctx, store.MemeSeen{
		Hash:        hash,
		PHash:       phashHex,
		Status:      "approved",
		Label:       m.Description,
		CreatedAtMs: nowMs,
	}); err != nil {
		return IngestResult{}, fmt.Errorf("meme: mark approved seen: %w", err)
	}

	v.logger.Info("meme ingested",
		slog.String("component", "meme"),
		slog.String("hash", ShortHash(hash)),
		slog.Bool("animated", isAnimated),
		slog.Int("embedding_dim", m.EmbeddingDim),
	)
	return IngestResult{Meme: m, Status: IngestStatusIngested}, nil
}

// RetrieveHand performs hybrid keyword + cosine-similarity search and returns the ranked items and <available_memes> block.
func (v *Vault) RetrieveHand(ctx context.Context, queryText string, queryVec []float32, limit int) ([]HandItem, string, error) {
	if limit <= 0 {
		limit = 6
	}
	active, err := v.store.ListActiveMemes(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("meme: list active memes: %w", err)
	}
	if len(active) == 0 {
		return nil, "", nil
	}

	qTrim := strings.TrimSpace(queryText)
	qLower := strings.ToLower(qTrim)
	qTokens := tokenizeText(qLower)
	hasText := qLower != ""
	hasVec := len(queryVec) > 0

	items := make([]HandItem, 0, len(active))
	for _, m := range active {
		kwScore := 0.0
		if hasText {
			kwScore = keywordScore(qLower, qTokens, m)
		}
		vecScore := 0.0
		vecUsed := false
		if hasVec && len(m.Embedding) == len(queryVec) {
			vecScore = max(0.0, cosineSimilarity(queryVec, m.Embedding))
			vecUsed = true
		}

		var score float64
		switch {
		case hasText && vecUsed:
			score = 0.65*vecScore + 0.35*kwScore
		case vecUsed:
			score = vecScore
		case hasText:
			score = kwScore
		default:
			score = 0.1
		}
		// Mild usage bonus acts as a tie-breaker without letting popular memes dominate irrelevant queries.
		score += 0.02 * min(1.0, math.Log1p(float64(max(0, m.UseCount)))/5.0)

		items = append(items, HandItem{
			Meme:         m,
			ShortID:      ShortHash(m.Hash),
			Score:        score,
			KeywordScore: kwScore,
			VectorScore:  vecScore,
		})
	}

	slices.SortFunc(items, func(a, b HandItem) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Meme.UseCount, a.Meme.UseCount); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Meme.UpdatedAtMs, a.Meme.UpdatedAtMs); c != 0 {
			return c
		}
		return cmp.Compare(a.Meme.Hash, b.Meme.Hash)
	})

	if len(items) > limit {
		items = items[:limit]
	}
	return items, FormatAvailableMemes(items), nil
}

// FormatAvailableMemes renders hand items into the prompt <available_memes> block.
func FormatAvailableMemes(items []HandItem) string {
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<available_memes>\n")
	for _, item := range items {
		m := item.Meme
		shortID := item.ShortID
		if shortID == "" {
			shortID = ShortHash(m.Hash)
		}
		sb.WriteString("[meme:#")
		sb.WriteString(shortID)
		sb.WriteString("]")

		var parts []string
		if d := strings.TrimSpace(m.Description); d != "" {
			parts = append(parts, d)
		}
		if e := strings.TrimSpace(m.Emotions); e != "" {
			parts = append(parts, "情绪:"+e)
		}
		if s := strings.TrimSpace(m.Scenes); s != "" {
			parts = append(parts, "场景:"+s)
		}
		if o := strings.TrimSpace(m.OverlayText); o != "" {
			parts = append(parts, "配文:"+o)
		}
		if len(parts) > 0 {
			sb.WriteByte(' ')
			sb.WriteString(strings.Join(parts, " | "))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("</available_memes>")
	return sb.String()
}

// UpdateImpression updates visual metadata for an active meme and re-embeds its search blob when configured.
func (v *Vault) UpdateImpression(ctx context.Context, hashOrPrefix string, patch ImpressionPatch) (store.Meme, error) {
	m, err := v.resolveMeme(ctx, hashOrPrefix)
	if err != nil {
		return store.Meme{}, err
	}
	if patch.OverlayText != nil {
		m.OverlayText = strings.TrimSpace(*patch.OverlayText)
	}
	if patch.Description != nil {
		m.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Emotions != nil {
		m.Emotions = strings.TrimSpace(*patch.Emotions)
	}
	if patch.Scenes != nil {
		m.Scenes = strings.TrimSpace(*patch.Scenes)
	}
	m.SearchBlob = buildSearchBlob(m.OverlayText, m.Description, m.Emotions, m.Scenes, "")
	if v.embedder != nil && m.SearchBlob != "" {
		vec, err := v.embedder.Embed(ctx, m.SearchBlob)
		if err != nil {
			return store.Meme{}, fmt.Errorf("meme: re-embed updated meme %q: %w", m.Hash, err)
		}
		m.Embedding = vec
		m.EmbeddingDim = len(vec)
	}
	m.UpdatedAtMs = time.Now().UnixMilli()
	if err := v.store.UpsertMeme(ctx, m); err != nil {
		return store.Meme{}, fmt.Errorf("meme: save updated meme %q: %w", m.Hash, err)
	}
	return m, nil
}

// Delete soft-deletes a meme and blacklists it in meme_seen so it will not be re-ingested.
func (v *Vault) Delete(ctx context.Context, hashOrPrefix string) (store.Meme, error) {
	m, err := v.resolveMeme(ctx, hashOrPrefix)
	if err != nil {
		return store.Meme{}, err
	}
	if err := v.store.DeleteMeme(ctx, m.Hash, time.Now().UnixMilli()); err != nil {
		return store.Meme{}, fmt.Errorf("meme: delete %q: %w", m.Hash, err)
	}
	if ph, perr := ParsePHash(m.PHash); perr == nil {
		v.mu.Lock()
		v.blockedPHashes = append(v.blockedPHashes, ph)
		v.mu.Unlock()
	}
	m.Status = store.MemeStatusDeleted
	return m, nil
}

// RecordUse increments use_count for a meme resolved by full hash or prefix.
func (v *Vault) RecordUse(ctx context.Context, hashOrPrefix string) error {
	m, err := v.resolveMeme(ctx, hashOrPrefix)
	if err != nil {
		return err
	}
	return v.store.IncrementMemeUse(ctx, m.Hash, time.Now().UnixMilli())
}

// Search searches active memes by keyword (plus optional embedding when available) for tool queries.
func (v *Vault) Search(ctx context.Context, query string, limit int) ([]HandItem, error) {
	if limit <= 0 {
		limit = 10
	}
	var queryVec []float32
	if q := strings.TrimSpace(query); q != "" && v.embedder != nil {
		if vec, err := v.embedder.Embed(ctx, q); err == nil {
			queryVec = vec
		}
	}
	items, _, err := v.RetrieveHand(ctx, query, queryVec, limit)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) != "" {
		filtered := make([]HandItem, 0, len(items))
		for _, item := range items {
			if item.KeywordScore > 0 || item.VectorScore > 0.15 {
				filtered = append(filtered, item)
			}
		}
		return filtered, nil
	}
	return items, nil
}

func ShortHash(hash string) string {
	hash = strings.TrimPrefix(strings.TrimSpace(hash), "#")
	if len(hash) <= 6 {
		return hash
	}
	return hash[:6]
}

func (v *Vault) resolveMeme(ctx context.Context, hashOrPrefix string) (store.Meme, error) {
	clean := strings.TrimPrefix(strings.TrimSpace(hashOrPrefix), "#")
	if clean == "" {
		return store.Meme{}, ErrMemeNotFound
	}
	if m, ok, err := v.store.GetMeme(ctx, clean); err != nil {
		return store.Meme{}, fmt.Errorf("meme: lookup %q: %w", clean, err)
	} else if ok && m.Status == store.MemeStatusActive {
		return m, nil
	}
	m, ok, err := v.store.FindMemeByPrefix(ctx, clean)
	if err != nil {
		return store.Meme{}, fmt.Errorf("meme: lookup prefix %q: %w", clean, err)
	}
	if !ok {
		return store.Meme{}, fmt.Errorf("%w: %q", ErrMemeNotFound, hashOrPrefix)
	}
	return m, nil
}

func (v *Vault) isPHashBlocked(ph uint64) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for _, blocked := range v.blockedPHashes {
		if HammingDistance(ph, blocked) <= v.dedupDistance {
			return true
		}
	}
	return false
}

func isBlockedSeenStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case store.MemeStatusDeleted, "blacklisted", "rejected":
		return true
	default:
		return false
	}
}

func sanitizeExt(ext string, raw []byte) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	ext = strings.TrimPrefix(ext, ".")
	if ext != "" && len(ext) <= 8 {
		valid := true
		for i := range len(ext) {
			c := ext[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				valid = false
				break
			}
		}
		if valid {
			return "." + ext
		}
	}
	switch http.DetectContentType(raw) {
	case "image/gif":
		return ".gif"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func buildSearchBlob(overlay, desc, emotions, scenes, hint string) string {
	fields := []string{desc, emotions, scenes, overlay, hint}
	seen := make(map[string]bool, len(fields))
	var parts []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		parts = append(parts, f)
	}
	return strings.Join(parts, " | ")
}

func keywordScore(qLower string, qTokens []string, m store.Meme) float64 {
	doc := strings.ToLower(strings.Join([]string{
		m.SearchBlob, m.Description, m.Emotions, m.Scenes, m.OverlayText,
	}, " "))
	if strings.TrimSpace(doc) == "" {
		return 0
	}
	if strings.Contains(doc, qLower) {
		return 1.0
	}
	if len(qTokens) == 0 {
		return 0
	}
	hits := 0
	for _, tok := range qTokens {
		if strings.Contains(doc, tok) {
			hits++
		}
	}
	return float64(hits) / float64(len(qTokens))
}

func tokenizeText(s string) []string {
	var tokens []string
	seen := make(map[string]bool)
	add := func(t string) {
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		tokens = append(tokens, t)
	}

	var asciiBuf strings.Builder
	var cjkRunes []rune
	flushASCII := func() {
		if asciiBuf.Len() > 0 {
			add(asciiBuf.String())
			asciiBuf.Reset()
		}
	}
	flushCJK := func() {
		if len(cjkRunes) == 0 {
			return
		}
		if len(cjkRunes) == 1 {
			add(string(cjkRunes[0]))
		} else {
			for i := 0; i < len(cjkRunes)-1; i++ {
				add(string(cjkRunes[i : i+2]))
			}
		}
		cjkRunes = cjkRunes[:0]
	}

	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			flushASCII()
			cjkRunes = append(cjkRunes, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			asciiBuf.WriteRune(unicode.ToLower(r))
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return tokens
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		va := float64(a[i])
		vb := float64(b[i])
		dot += va * vb
		normA += va * va
		normB += vb * vb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
