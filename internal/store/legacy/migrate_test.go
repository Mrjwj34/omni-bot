package legacy

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"omni-bot/internal/store"
)

func createSQLiteFixture(t *testing.T, dbPath string, stmts []string) {
	t.Helper()
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		t.Fatalf("abs %q: %v", dbPath, err)
	}
	slash := filepath.ToSlash(abs)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	dsn := (&url.URL{Scheme: "file", Path: slash}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open fixture %q: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range stmts {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("exec fixture stmt on %q: %v", dbPath, err)
		}
	}
}

func TestMigrateLegacyFixtureAndIdempotency(t *testing.T) {
	ctx := t.Context()
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// 1. Seed timeline.db
	createSQLiteFixture(t, filepath.Join(srcDir, "timeline.db"), []string{
		`CREATE TABLE group_timeline (
			seq_id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_id TEXT NOT NULL,
			msg_id TEXT NOT NULL DEFAULT '',
			sender_id TEXT NOT NULL,
			sender_name TEXT NOT NULL,
			is_bot INTEGER NOT NULL DEFAULT 0,
			is_at_bot INTEGER NOT NULL DEFAULT 0,
			timestamp REAL NOT NULL,
			rendered_text TEXT NOT NULL,
			image_paths_json TEXT NOT NULL DEFAULT '[]',
			audio_paths_json TEXT NOT NULL DEFAULT '[]',
			status TEXT NOT NULL DEFAULT 'UNPROCESSED',
			archived_to_memory INTEGER NOT NULL DEFAULT 0,
			media_refs_json TEXT NOT NULL DEFAULT '[]'
		)`,
		`CREATE TABLE group_summaries (
			group_id TEXT PRIMARY KEY,
			summary_text TEXT NOT NULL DEFAULT '',
			last_summarized_seq_id INTEGER NOT NULL DEFAULT 0,
			updated_at REAL NOT NULL DEFAULT 0
		)`,
		`INSERT INTO group_timeline (seq_id, group_id, msg_id, sender_id, sender_name, is_bot, is_at_bot, timestamp, rendered_text, status, archived_to_memory, media_refs_json)
		 VALUES (549, 'group-42', 'm1', 'u1', 'Alice', 0, 1, 1794555384.5, '@bk101 早安', 'PROCESSED', 1, '["https://example.invalid/files/1"]'),
		        (550, 'group-42', 'm2', 'bot', 'bk101', 1, 0, 1794555390.0, '早安呀', 'PROCESSED', 0, '[]')`,
		`INSERT INTO group_summaries (group_id, summary_text, last_summarized_seq_id, updated_at)
		 VALUES ('group-42', '大家早上打招呼。', 549, 1794555400.0)`,
	})

	// 2. Seed memes.db + memes_store/ + memes_thumbs/
	if err := os.MkdirAll(filepath.Join(srcDir, "memes_store"), 0o755); err != nil {
		t.Fatalf("mkdir memes_store: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "memes_thumbs"), 0o755); err != nil {
		t.Fatalf("mkdir memes_thumbs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "memes_store", "abcd1234.gif"), []byte("GIF89a-meme"), 0o644); err != nil {
		t.Fatalf("write meme image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "memes_thumbs", "abcd1234.webp"), []byte("RIFF-thumb"), 0o644); err != nil {
		t.Fatalf("write meme thumb: %v", err)
	}

	vec4 := store.EncodeFloat32LE([]float32{0.5, 0.5, 0.5, 0.5})
	createSQLiteFixture(t, filepath.Join(srcDir, "memes.db"), []string{
		`CREATE TABLE memes (
			hash TEXT PRIMARY KEY,
			path TEXT NOT NULL,
			overlay_text TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			emotions TEXT NOT NULL DEFAULT '',
			scenes TEXT NOT NULL DEFAULT '',
			search_blob TEXT NOT NULL DEFAULT '',
			embedding BLOB,
			dim INTEGER NOT NULL DEFAULT 0,
			use_count INTEGER NOT NULL DEFAULT 0,
			created_at REAL NOT NULL,
			phash TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE meme_seen (
			hash TEXT PRIMARY KEY,
			phash TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'seen',
			label TEXT NOT NULL DEFAULT '',
			created_at REAL NOT NULL
		)`,
		`INSERT INTO meme_seen (hash, phash, status, label, created_at)
		 VALUES ('abcd1234', 'ph_1234', 'approved', 'ok', 1794555384.0),
		        ('deadbeef', 'ph_dead', 'deleted', 'ban', 1794555385.0)`,
	})
	// Insert meme row with binary embedding blob via parameter binding.
	{
		abs := filepath.ToSlash(filepath.Join(srcDir, "memes.db"))
		if !strings.HasPrefix(abs, "/") {
			abs = "/" + abs
		}
		db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs}).String())
		if err != nil {
			t.Fatalf("open memes.db: %v", err)
		}
		_, err = db.ExecContext(ctx, `
			INSERT INTO memes (hash, path, overlay_text, description, emotions, scenes, search_blob, embedding, dim, use_count, created_at, phash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"abcd1234", "/AstrBot/data/plugin_data/astrbot_plugin_omni_group/memes_store/abcd1234.gif",
			"确实", "点头赞同的小猫", "赞同,开心", "附和群友", "确实 点头赞同的小猫", vec4, 4, 7, 1794555384.0, "ph_1234",
		)
		_ = db.Close()
		if err != nil {
			t.Fatalf("insert meme fixture: %v", err)
		}
	}

	// 3. Seed data_v4.db with bk101 persona
	const legacyPrompt = "你是常驻在这个熟人 QQ 群里的少女群友，平时在群里就以自己的群 ID（bk101 / 101）为准。"
	createSQLiteFixture(t, filepath.Join(srcDir, "data_v4.db"), []string{
		`CREATE TABLE personas (
			id INTEGER PRIMARY KEY,
			persona_id VARCHAR(255) NOT NULL,
			system_prompt TEXT NOT NULL
		)`,
		`INSERT INTO personas (id, persona_id, system_prompt) VALUES (1, 'default', '` + legacyPrompt + `')`,
	})

	// 4. Seed optional mem0_export.jsonl with a 2560d embedding to verify 1024d Matryoshka truncation.
	bigVec := make([]float32, 2560)
	bigVec[0] = 3.0
	bigVec[1] = 4.0
	rawMemLine, err := json.Marshal(map[string]any{
		"uuid":       "mem-uuid-1",
		"user_id":    "group_group-42",
		"data":       "Alice 喜欢吃抹茶冰淇淋",
		"created_at": "2026-11-18T10:00:00Z",
		"embedding":  bigVec,
	})
	if err != nil {
		t.Fatalf("marshal mem0 fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "mem0_export.jsonl"), append(rawMemLine, '\n'), 0o644); err != nil {
		t.Fatalf("write mem0_export.jsonl: %v", err)
	}

	targetStore, err := store.Open(ctx, filepath.Join(dstDir, "omni.db"))
	if err != nil {
		t.Fatalf("open target store: %v", err)
	}
	defer func() { _ = targetStore.Close() }()
	targetBlobs, err := store.NewBlobStore(dstDir)
	if err != nil {
		t.Fatalf("NewBlobStore: %v", err)
	}

	opts := Options{
		SourceDir:   srcDir,
		TargetStore: targetStore,
		TargetBlobs: targetBlobs,
	}

	// First run
	rep1, err := MigrateLegacy(ctx, opts)
	if err != nil {
		t.Fatalf("MigrateLegacy run 1: %v", err)
	}
	if rep1.TimelineImported != 2 || rep1.SummariesImported != 1 || rep1.MemesImported != 1 ||
		rep1.MemeFilesCopied != 1 || rep1.MemeThumbsCopied != 1 || rep1.MemeSeenImported != 2 ||
		rep1.MemoriesImported != 1 || !rep1.PersonaImported || rep1.PersonaTraitsSeeded != 9 {
		t.Fatalf("unexpected run 1 report: %+v", rep1)
	}

	// Second run must be idempotent (zero duplicate snapshots or rows).
	rep2, err := MigrateLegacy(ctx, opts)
	if err != nil {
		t.Fatalf("MigrateLegacy run 2: %v", err)
	}
	if rep2.PersonaTraitsSeeded != 0 {
		t.Fatalf("expected second run not to re-seed traits, got %d", rep2.PersonaTraitsSeeded)
	}

	// Verify timeline & summary
	msgs, err := targetStore.ListRecentTimeline(ctx, "group-42", 10)
	if err != nil || len(msgs) != 2 || msgs[0].SeqID != 549 || msgs[1].SeqID != 550 {
		t.Fatalf("migrated timeline = %+v err=%v", msgs, err)
	}
	summary, ok, err := targetStore.GetRoomSummary(ctx, "group-42")
	if err != nil || !ok || summary.LastSummarizedSeq != 549 {
		t.Fatalf("migrated summary = (%+v, %v, %v)", summary, ok, err)
	}

	// Verify meme record & copied files
	meme, ok, err := targetStore.GetMeme(ctx, "abcd1234")
	if err != nil || !ok || meme.RelPath != "memes/abcd1234.gif" || meme.ThumbRelPath != "memes/thumbs/abcd1234.webp" || meme.UseCount != 7 {
		t.Fatalf("migrated meme = (%+v, %v, %v)", meme, ok, err)
	}
	rcImg, err := targetBlobs.OpenMemeFile(ctx, meme.RelPath)
	if err != nil {
		t.Fatalf("OpenMemeFile img: %v", err)
	}
	imgBytes, _ := io.ReadAll(rcImg)
	_ = rcImg.Close()
	if string(imgBytes) != "GIF89a-meme" {
		t.Fatalf("copied meme image mismatch: %q", imgBytes)
	}
	rcThumb, err := targetBlobs.OpenMemeFile(ctx, meme.ThumbRelPath)
	if err != nil {
		t.Fatalf("OpenMemeFile thumb: %v", err)
	}
	thumbBytes, _ := io.ReadAll(rcThumb)
	_ = rcThumb.Close()
	if string(thumbBytes) != "RIFF-thumb" {
		t.Fatalf("copied meme thumb mismatch: %q", thumbBytes)
	}

	// Verify persona preservation & 1st-person 3-layer defaults
	core, ok, err := targetStore.GetPersonaCore(ctx, store.DefaultPersonaRoomID)
	if err != nil || !ok || core.LegacyPrompt != legacyPrompt || !strings.Contains(core.Beliefs, "我是常驻在这个熟人") {
		t.Fatalf("migrated persona core = (%+v, %v, %v)", core, ok, err)
	}
	snaps, err := targetStore.ListPersonaSnapshots(ctx, 10)
	if err != nil || len(snaps) != 1 || snaps[0].Reason != "legacy_migration" {
		t.Fatalf("expected exactly 1 migration snapshot after 2 runs, got %+v err=%v", snaps, err)
	}

	// Verify mem0 FTS + 1024d truncated vector
	hits, err := targetStore.SearchMemoriesFTS(ctx, "group-42", "抹茶冰淇淋", 5, false)
	if err != nil || len(hits) != 1 || hits[0].Memory.ID != "mem-uuid-1" {
		t.Fatalf("migrated memory FTS hits = %+v err=%v", hits, err)
	}
	vecs, err := targetStore.LoadActiveMemoryVectors(ctx, "group-42")
	if err != nil || len(vecs) != 1 || len(vecs[0].Embedding) != 1024 {
		t.Fatalf("expected 1024d truncated vector, got len=%d err=%v", len(vecs), err)
	}
}

func TestMigrateLegacySnapshotIfPresent(t *testing.T) {
	snapshotDir := findLegacySnapshotDir(t)
	if snapshotDir == "" {
		t.Skip("legacy snapshot not present; skipping gated snapshot test")
	}

	// Fingerprint core snapshot DB files before migration to prove strictly read-only access.
	watched := []string{"timeline.db", "memes.db", "data_v4.db"}
	beforeDigests := make(map[string]string, len(watched))
	for _, name := range watched {
		beforeDigests[name] = hashFile(t, filepath.Join(snapshotDir, name))
	}

	ctx := t.Context()
	dstDir := t.TempDir()
	targetStore, err := store.Open(ctx, filepath.Join(dstDir, "omni.db"))
	if err != nil {
		t.Fatalf("open target store: %v", err)
	}
	defer func() { _ = targetStore.Close() }()
	targetBlobs, err := store.NewBlobStore(dstDir)
	if err != nil {
		t.Fatalf("NewBlobStore: %v", err)
	}

	opts := Options{
		SourceDir:   snapshotDir,
		TargetStore: targetStore,
		TargetBlobs: targetBlobs,
	}

	rep, err := MigrateLegacy(ctx, opts)
	if err != nil {
		t.Fatalf("MigrateLegacy on snapshot: %v", err)
	}
	if rep.TimelineImported < 5000 {
		t.Errorf("TimelineImported = %d, want >= 5000", rep.TimelineImported)
	}
	if rep.SummariesImported < 1 {
		t.Errorf("SummariesImported = %d, want >= 1", rep.SummariesImported)
	}
	if rep.MemesImported != 64 || rep.MemeFilesCopied != 64 {
		t.Errorf("MemesImported=%d MemeFilesCopied=%d, want 64/64", rep.MemesImported, rep.MemeFilesCopied)
	}
	if rep.MemeThumbsCopied != 49 {
		t.Errorf("MemeThumbsCopied = %d, want 49", rep.MemeThumbsCopied)
	}
	if rep.MemeSeenImported != 131 {
		t.Errorf("MemeSeenImported = %d, want 131", rep.MemeSeenImported)
	}
	if !rep.PersonaImported || rep.PersonaTraitsSeeded != 9 {
		t.Errorf("PersonaImported=%v PersonaTraitsSeeded=%d, want true/9", rep.PersonaImported, rep.PersonaTraitsSeeded)
	}

	// Verify idempotent re-run on real snapshot.
	rep2, err := MigrateLegacy(ctx, opts)
	if err != nil {
		t.Fatalf("MigrateLegacy second run on snapshot: %v", err)
	}
	if rep2.PersonaTraitsSeeded != 0 {
		t.Errorf("second run re-seeded traits: %d", rep2.PersonaTraitsSeeded)
	}

	// Verify snapshot files were never modified.
	for _, name := range watched {
		after := hashFile(t, filepath.Join(snapshotDir, name))
		if after != beforeDigests[name] {
			t.Fatalf("snapshot file %q was modified during read-only migration!", name)
		}
	}
}

func findLegacySnapshotDir(t *testing.T) string {
	t.Helper()
	candidates := []string{
		os.Getenv("OMNI_LEGACY_SNAPSHOT_DIR"),
		filepath.Join("..", "..", "..", "data", "legacy_snapshot"),
	}
	if out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--git-common-dir").Output(); err == nil {
		commonDir := strings.TrimSpace(string(out))
		if commonDir != "" {
			if absCommon, err := filepath.Abs(commonDir); err == nil {
				candidates = append(candidates, filepath.Join(filepath.Dir(absCommon), "data", "legacy_snapshot"))
			}
		}
	}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "timeline.db")); err == nil {
			return dir
		}
	}
	return ""
}

func hashFile(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open %q: %v", p, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hash %q: %v", p, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
