package legacy

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"omni-bot/internal/store"
)

// Options configures a read-only idempotent migration from a legacy AstrBot data directory.
type Options struct {
	SourceDir          string
	TargetStore        *store.Store
	TargetBlobs        *store.BlobStore
	DefaultChannelID   string
	PersonaRoomID      string
	TargetEmbeddingDim int
}

// Report summarizes counts and non-fatal warnings produced by MigrateLegacy.
type Report struct {
	TimelineImported    int      `json:"timeline_imported"`
	SummariesImported   int      `json:"summaries_imported"`
	MemesImported       int      `json:"memes_imported"`
	MemeFilesCopied     int      `json:"meme_files_copied"`
	MemeThumbsCopied    int      `json:"meme_thumbs_copied"`
	MemeSeenImported    int      `json:"meme_seen_imported"`
	MemoriesImported    int      `json:"memories_imported"`
	PersonaImported     bool     `json:"persona_imported"`
	PersonaTraitsSeeded int      `json:"persona_traits_seeded"`
	Warnings            []string `json:"warnings,omitempty"`
}

// MigrateLegacy idempotently imports timeline, summaries, memes, persona, and optional mem0 JSONL
// from a legacy AstrBot directory without modifying any files under opts.SourceDir.
func MigrateLegacy(ctx context.Context, opts Options) (Report, error) {
	if strings.TrimSpace(opts.SourceDir) == "" {
		return Report{}, errors.New("legacy: SourceDir must not be empty")
	}
	if opts.TargetStore == nil {
		return Report{}, errors.New("legacy: TargetStore must not be nil")
	}
	if opts.TargetBlobs == nil {
		return Report{}, errors.New("legacy: TargetBlobs must not be nil")
	}
	if opts.DefaultChannelID == "" {
		opts.DefaultChannelID = "napcat"
	}
	if opts.PersonaRoomID == "" {
		opts.PersonaRoomID = store.DefaultPersonaRoomID
	}
	if opts.TargetEmbeddingDim == 0 {
		opts.TargetEmbeddingDim = 1024
	}

	info, err := os.Stat(opts.SourceDir)
	if err != nil {
		return Report{}, fmt.Errorf("legacy: stat source dir %q: %w", opts.SourceDir, err)
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("legacy: source path %q is not a directory", opts.SourceDir)
	}

	var report Report

	if err := importTimeline(ctx, opts, &report); err != nil {
		return report, err
	}
	if err := importMemes(ctx, opts, &report); err != nil {
		return report, err
	}
	if err := importPersona(ctx, opts, &report); err != nil {
		return report, err
	}
	if err := importMem0JSONL(ctx, opts, &report); err != nil {
		return report, err
	}

	return report, nil
}

func importTimeline(ctx context.Context, opts Options, report *Report) error {
	dbPath := filepath.Join(opts.SourceDir, "timeline.db")
	if !fileExists(dbPath) {
		report.Warnings = append(report.Warnings, "timeline.db not found; skipped timeline import")
		return nil
	}
	db, err := openReadOnlySQLite(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("legacy: open timeline.db: %w", err)
	}
	defer func() { _ = db.Close() }()

	if tableExists(ctx, db, "group_timeline") {
		rows, err := db.QueryContext(ctx, `
			SELECT seq_id, group_id, msg_id, sender_id, sender_name,
			       is_bot, is_at_bot, timestamp, rendered_text,
			       image_paths_json, audio_paths_json, status, archived_to_memory, media_refs_json
			FROM group_timeline
			ORDER BY seq_id ASC`)
		if err != nil {
			return fmt.Errorf("legacy: query group_timeline: %w", err)
		}
		defer rows.Close()

		const batchSize = 500
		batch := make([]store.TimelineMessage, 0, batchSize)
		for rows.Next() {
			var seqID int64
			var groupID, msgID, senderID, senderName, renderedText string
			var imgJSON, audJSON, status, mediaRefsJSON string
			var isBot, isAtBot, archived int
			var tsSec float64
			if err := rows.Scan(
				&seqID, &groupID, &msgID, &senderID, &senderName,
				&isBot, &isAtBot, &tsSec, &renderedText,
				&imgJSON, &audJSON, &status, &archived, &mediaRefsJSON,
			); err != nil {
				return fmt.Errorf("legacy: scan group_timeline: %w", err)
			}

			batch = append(batch, store.TimelineMessage{
				SeqID:            seqID,
				ChannelID:        opts.DefaultChannelID,
				RoomID:           groupID,
				MsgID:            msgID,
				SenderID:         senderID,
				SenderName:       senderName,
				IsBot:            isBot != 0,
				IsAtBot:          isAtBot != 0,
				TimestampMs:      secToMs(tsSec),
				ContentJSON:      buildLegacyContentJSON(renderedText, imgJSON, audJSON, mediaRefsJSON),
				RenderedText:     renderedText,
				Status:           status,
				ArchivedToMemory: archived != 0,
			})
			report.TimelineImported++

			if len(batch) >= batchSize {
				if err := opts.TargetStore.UpsertTimelineBatch(ctx, batch); err != nil {
					return fmt.Errorf("legacy: write timeline batch: %w", err)
				}
				batch = batch[:0]
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("legacy: iterate group_timeline: %w", err)
		}
		if len(batch) > 0 {
			if err := opts.TargetStore.UpsertTimelineBatch(ctx, batch); err != nil {
				return fmt.Errorf("legacy: write final timeline batch: %w", err)
			}
		}
	}

	if tableExists(ctx, db, "group_summaries") {
		rows, err := db.QueryContext(ctx, `
			SELECT group_id, summary_text, last_summarized_seq_id, updated_at
			FROM group_summaries`)
		if err != nil {
			return fmt.Errorf("legacy: query group_summaries: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var groupID, summaryText string
			var lastSeq int64
			var updatedSec float64
			if err := rows.Scan(&groupID, &summaryText, &lastSeq, &updatedSec); err != nil {
				return fmt.Errorf("legacy: scan group_summaries: %w", err)
			}
			if err := opts.TargetStore.UpsertRoomSummary(ctx, store.RoomSummary{
				RoomID:            groupID,
				SummaryText:       summaryText,
				LastSummarizedSeq: lastSeq,
				UpdatedAtMs:       secToMs(updatedSec),
			}); err != nil {
				return fmt.Errorf("legacy: upsert room summary %q: %w", groupID, err)
			}
			report.SummariesImported++
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("legacy: iterate group_summaries: %w", err)
		}
	}

	return nil
}

func importMemes(ctx context.Context, opts Options, report *Report) error {
	dbPath := filepath.Join(opts.SourceDir, "memes.db")
	if !fileExists(dbPath) {
		report.Warnings = append(report.Warnings, "memes.db not found; skipped meme import")
		return nil
	}
	db, err := openReadOnlySQLite(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("legacy: open memes.db: %w", err)
	}
	defer func() { _ = db.Close() }()

	if tableExists(ctx, db, "memes") {
		rows, err := db.QueryContext(ctx, `
			SELECT hash, path, overlay_text, description, emotions, scenes,
			       search_blob, embedding, dim, use_count, created_at, phash
			FROM memes`)
		if err != nil {
			return fmt.Errorf("legacy: query memes: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var hash, containerPath, overlay, desc, emotions, scenes, searchBlob, phash string
			var embRaw []byte
			var dim, useCount int
			var createdSec float64
			if err := rows.Scan(
				&hash, &containerPath, &overlay, &desc, &emotions, &scenes,
				&searchBlob, &embRaw, &dim, &useCount, &createdSec, &phash,
			); err != nil {
				return fmt.Errorf("legacy: scan memes: %w", err)
			}

			baseName := extractSafeBasename(containerPath)
			if baseName == "" {
				baseName = hash
			}
			relPath := path.Join("memes", baseName)

			srcImgPath := filepath.Join(opts.SourceDir, "memes_store", baseName)
			if fileExists(srcImgPath) {
				copiedRel, err := copyFileToMemeStore(ctx, opts.TargetBlobs, baseName, srcImgPath)
				if err != nil {
					return fmt.Errorf("legacy: copy meme %q: %w", baseName, err)
				}
				relPath = copiedRel
				report.MemeFilesCopied++
			} else {
				report.Warnings = append(report.Warnings, fmt.Sprintf("missing meme file %s for hash %s", baseName, hash))
			}

			thumbRelPath := ""
			thumbName := hash + ".webp"
			srcThumbPath := filepath.Join(opts.SourceDir, "memes_thumbs", thumbName)
			if fileExists(srcThumbPath) {
				copiedThumb, err := copyFileToMemeStore(ctx, opts.TargetBlobs, path.Join("thumbs", thumbName), srcThumbPath)
				if err != nil {
					return fmt.Errorf("legacy: copy meme thumb %q: %w", thumbName, err)
				}
				thumbRelPath = copiedThumb
				report.MemeThumbsCopied++
			}

			var vec []float32
			if len(embRaw) > 0 {
				vec, err = store.DecodeFloat32LE(embRaw)
				if err != nil {
					return fmt.Errorf("legacy: decode meme embedding %q: %w", hash, err)
				}
			}
			createdMs := secToMs(createdSec)
			if err := opts.TargetStore.UpsertMeme(ctx, store.Meme{
				Hash:         hash,
				RelPath:      relPath,
				ThumbRelPath: thumbRelPath,
				PHash:        phash,
				OverlayText:  overlay,
				Description:  desc,
				Emotions:     emotions,
				Scenes:       scenes,
				SearchBlob:   searchBlob,
				Embedding:    vec,
				EmbeddingDim: dim,
				UseCount:     useCount,
				Status:       store.MemeStatusActive,
				CreatedAtMs:  createdMs,
				UpdatedAtMs:  createdMs,
			}); err != nil {
				return fmt.Errorf("legacy: upsert meme %q: %w", hash, err)
			}
			report.MemesImported++
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("legacy: iterate memes: %w", err)
		}
	}

	if tableExists(ctx, db, "meme_seen") {
		rows, err := db.QueryContext(ctx, `SELECT hash, phash, status, label, created_at FROM meme_seen`)
		if err != nil {
			return fmt.Errorf("legacy: query meme_seen: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var hash, phash, status, label string
			var createdSec float64
			if err := rows.Scan(&hash, &phash, &status, &label, &createdSec); err != nil {
				return fmt.Errorf("legacy: scan meme_seen: %w", err)
			}
			if err := opts.TargetStore.MarkMemeSeen(ctx, store.MemeSeen{
				Hash:        hash,
				PHash:       phash,
				Status:      status,
				Label:       label,
				CreatedAtMs: secToMs(createdSec),
			}); err != nil {
				return fmt.Errorf("legacy: upsert meme_seen %q: %w", hash, err)
			}
			report.MemeSeenImported++
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("legacy: iterate meme_seen: %w", err)
		}
	}

	return nil
}

func importPersona(ctx context.Context, opts Options, report *Report) error {
	dbPath := filepath.Join(opts.SourceDir, "data_v4.db")
	if !fileExists(dbPath) {
		report.Warnings = append(report.Warnings, "data_v4.db not found; skipped persona import")
		return nil
	}
	db, err := openReadOnlySQLite(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("legacy: open data_v4.db: %w", err)
	}
	defer func() { _ = db.Close() }()

	if !tableExists(ctx, db, "personas") {
		report.Warnings = append(report.Warnings, "personas table missing in data_v4.db")
		return nil
	}

	var personaID, legacyPrompt string
	err = db.QueryRowContext(ctx, `
		SELECT persona_id, system_prompt
		FROM personas
		ORDER BY CASE WHEN persona_id = 'default' THEN 0 ELSE 1 END, id ASC
		LIMIT 1`).Scan(&personaID, &legacyPrompt)
	if errors.Is(err, sql.ErrNoRows) {
		report.Warnings = append(report.Warnings, "no persona rows in data_v4.db")
		return nil
	}
	if err != nil {
		return fmt.Errorf("legacy: query personas: %w", err)
	}

	nowMs := time.Now().UnixMilli()
	existingCore, hasCore, err := opts.TargetStore.GetPersonaCore(ctx, opts.PersonaRoomID)
	if err != nil {
		return fmt.Errorf("legacy: check existing persona core: %w", err)
	}

	if !hasCore {
		beliefs := strings.TrimSpace(legacyPrompt)
		if isBK101LegacyPrompt(legacyPrompt) {
			beliefs = defaultBK101Beliefs
		}
		core := store.PersonaCore{
			RoomID:       opts.PersonaRoomID,
			Beliefs:      beliefs,
			Temperament:  defaultTemperamentParams(),
			LegacyPrompt: legacyPrompt,
			UpdatedAtMs:  nowMs,
		}
		if err := opts.TargetStore.SetPersonaCore(ctx, core, ""); err != nil {
			return fmt.Errorf("legacy: write persona core: %w", err)
		}
	} else if existingCore.LegacyPrompt == "" && legacyPrompt != "" {
		existingCore.LegacyPrompt = legacyPrompt
		existingCore.UpdatedAtMs = nowMs
		if err := opts.TargetStore.SetPersonaCore(ctx, existingCore, ""); err != nil {
			return fmt.Errorf("legacy: backfill legacy_prompt: %w", err)
		}
	}
	report.PersonaImported = true

	// Seed 1st-person atomic traits once; re-running migration must not overwrite evolved traits.
	existingTraits, err := opts.TargetStore.ListPersonaTraits(ctx)
	if err != nil {
		return fmt.Errorf("legacy: list existing persona traits: %w", err)
	}
	if len(existingTraits) == 0 && isBK101LegacyPrompt(legacyPrompt) {
		traits := buildBK101SeedTraits(nowMs)
		if _, err := opts.TargetStore.ReplacePersonaTraits(ctx, opts.PersonaRoomID, traits, "legacy_migration"); err != nil {
			return fmt.Errorf("legacy: seed persona traits: %w", err)
		}
		report.PersonaTraitsSeeded = len(traits)
	}

	return nil
}

type legacyMem0Record struct {
	ID         string    `json:"id"`
	UUID       string    `json:"uuid"`
	UserID     string    `json:"user_id"`
	RoomID     string    `json:"room_id"`
	Kind       string    `json:"kind"`
	Data       string    `json:"data"`
	Content    string    `json:"content"`
	Hash       string    `json:"hash"`
	Importance int       `json:"importance"`
	CreatedAt  any       `json:"created_at"`
	UpdatedAt  any       `json:"updated_at"`
	Entities   []string  `json:"entities"`
	Embedding  []float32 `json:"embedding"`
}

func importMem0JSONL(ctx context.Context, opts Options, report *Report) error {
	jsonlPath := filepath.Join(opts.SourceDir, "mem0_export.jsonl")
	if !fileExists(jsonlPath) {
		return nil
	}
	f, err := os.Open(jsonlPath)
	if err != nil {
		return fmt.Errorf("legacy: open mem0_export.jsonl: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	// Allow lines carrying 2560-dim JSON float arrays.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec legacyMem0Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return fmt.Errorf("legacy: parse mem0_export.jsonl line %d: %w", lineNo, err)
		}

		id := firstNonEmpty(rec.ID, rec.UUID, rec.Hash)
		content := firstNonEmpty(rec.Data, rec.Content)
		if id == "" || content == "" {
			report.Warnings = append(report.Warnings, fmt.Sprintf("mem0_export.jsonl line %d missing id or content; skipped", lineNo))
			continue
		}

		roomID := rec.RoomID
		entities := append([]string(nil), rec.Entities...)
		if roomID == "" {
			if gid, ok := strings.CutPrefix(rec.UserID, "group_"); ok && gid != "" {
				roomID = gid
			} else {
				roomID = opts.PersonaRoomID
				if rec.UserID != "" {
					entities = append(entities, rec.UserID)
				}
			}
		}

		kind := rec.Kind
		if kind == "" {
			kind = store.MemoryKindSemantic
		}
		importance := rec.Importance
		if importance <= 0 {
			importance = 5
		}

		createdMs := parseFlexibleTimestampMs(rec.CreatedAt)
		updatedMs := parseFlexibleTimestampMs(rec.UpdatedAt)
		if createdMs == 0 {
			createdMs = time.Now().UnixMilli()
		}
		if updatedMs == 0 {
			updatedMs = createdMs
		}

		vec := truncateAndNormalize(rec.Embedding, opts.TargetEmbeddingDim)
		if err := opts.TargetStore.UpsertMemory(ctx, store.Memory{
			ID:          id,
			RoomID:      roomID,
			Kind:        kind,
			Content:     content,
			TEventMs:    createdMs,
			CreatedAtMs: createdMs,
			UpdatedAtMs: updatedMs,
			Importance:  importance,
			Embedding:   vec,
			Entities:    entities,
		}); err != nil {
			return fmt.Errorf("legacy: upsert mem0 record %q: %w", id, err)
		}
		report.MemoriesImported++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("legacy: scan mem0_export.jsonl: %w", err)
	}
	return nil
}

func openReadOnlySQLite(ctx context.Context, dbPath string) (*sql.DB, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	slash := filepath.ToSlash(abs)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}

	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(ON)")
	q.Add("_pragma", "busy_timeout(5000)")
	// When no active WAL frames exist, immutable=1 prevents SQLite from creating/touching -shm files.
	if walInfo, err := os.Stat(abs + "-wal"); errors.Is(err, os.ErrNotExist) || (err == nil && walInfo.Size() == 0) {
		q.Set("immutable", "1")
	}

	dsn := (&url.URL{
		Scheme:   "file",
		Path:     slash,
		RawQuery: q.Encode(),
	}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func tableExists(ctx context.Context, db *sql.DB, name string) bool {
	var found string
	err := db.QueryRowContext(
		ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&found)
	return err == nil && found == name
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func secToMs(sec float64) int64 {
	if sec <= 0 {
		return 0
	}
	return int64(math.Round(sec * 1000))
}

func extractSafeBasename(rawPath string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(rawPath), "\\", "/")
	base := path.Base(normalized)
	if base == "" || base == "." || base == "/" || base == ".." {
		return ""
	}
	return base
}

func copyFileToMemeStore(ctx context.Context, blobs *store.BlobStore, relName, srcPath string) (string, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	rel, _, err := blobs.PutMemeFile(ctx, relName, f)
	return rel, err
}

func buildLegacyContentJSON(renderedText, imgJSON, audJSON, mediaRefsJSON string) string {
	type block struct {
		Type       string `json:"type"`
		Text       string `json:"text,omitempty"`
		LegacyPath string `json:"legacy_path,omitempty"`
		URI        string `json:"uri,omitempty"`
	}
	var blocks []block
	if renderedText != "" {
		blocks = append(blocks, block{Type: "text", Text: renderedText})
	}
	var imgs, auds, refs []string
	_ = json.Unmarshal([]byte(imgJSON), &imgs)
	_ = json.Unmarshal([]byte(audJSON), &auds)
	_ = json.Unmarshal([]byte(mediaRefsJSON), &refs)
	for _, p := range imgs {
		if p != "" {
			blocks = append(blocks, block{Type: "image", LegacyPath: p})
		}
	}
	for _, p := range auds {
		if p != "" {
			blocks = append(blocks, block{Type: "audio", LegacyPath: p})
		}
	}
	for _, u := range refs {
		if u != "" {
			blocks = append(blocks, block{Type: "media_ref", URI: u})
		}
	}
	if len(blocks) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func parseFlexibleTimestampMs(v any) int64 {
	switch val := v.(type) {
	case float64:
		if val <= 0 {
			return 0
		}
		if val > 1e12 {
			return int64(math.Round(val))
		}
		return int64(math.Round(val * 1000))
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return 0
		}
		for _, layout := range []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05.999999",
			"2006-01-02 15:04:05",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UnixMilli()
			}
		}
	}
	return 0
}

func truncateAndNormalize(vec []float32, targetDim int) []float32 {
	if len(vec) == 0 {
		return nil
	}
	out := vec
	if targetDim > 0 && len(vec) > targetDim {
		out = append([]float32(nil), vec[:targetDim]...)
	} else {
		out = append([]float32(nil), vec...)
	}
	var sumSq float64
	for _, x := range out {
		sumSq += float64(x) * float64(x)
	}
	if sumSq > 0 {
		inv := float32(1.0 / math.Sqrt(sumSq))
		for i := range out {
			out[i] *= inv
		}
	}
	return out
}
