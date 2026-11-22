package store

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestOpenWALPragmasAndIdempotentMigrations(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "omni.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var journalMode string
	if err := s.writeDB.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := s.writeDB.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}

	var foreignKeys int
	if err := s.writeDB.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	wantTables := []string{
		"timeline_messages",
		"room_summaries",
		"memories",
		"memories_fts",
		"memory_entities",
		"entity_aliases",
		"persona_core",
		"persona_traits",
		"persona_snapshots",
		"pad_states",
		"pad_history",
		"memes",
		"meme_seen",
		"run_traces",
		"dynamic_config",
		"secrets",
		"schema_migrations",
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var gotTables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		gotTables = append(gotTables, name)
	}
	_ = rows.Close()

	for _, want := range wantTables {
		if !slices.Contains(gotTables, want) {
			t.Errorf("missing expected table %q (got %v)", want, gotTables)
		}
	}
	if slices.Contains(gotTables, "memory_assoc") {
		t.Errorf("memory_assoc must not exist per Phase 0 KISS verdict")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close first open: %v", err)
	}

	// Re-open same file to verify idempotent migrations.
	s2, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("re-open Open: %v", err)
	}
	defer func() {
		if err := s2.Close(); err != nil {
			t.Errorf("Close second open: %v", err)
		}
	}()

	var migCount int
	if err := s2.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migCount != 1 {
		t.Fatalf("migration count = %d, want 1", migCount)
	}

	memStore, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open :memory:: %v", err)
	}
	if err := memStore.Close(); err != nil {
		t.Fatalf("Close :memory:: %v", err)
	}
}

func TestFTS5VirtualTableIndexing(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "fts.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	_, err = s.writeDB.ExecContext(
		ctx,
		`INSERT INTO memories_fts (memory_id, room_id, content_tokens, entities_tokens) VALUES (?, ?, ?, ?)`,
		"m1", "room1", TokenizeForFTS("小明最爱吃麻辣香锅和烤肉"), TokenizeForFTS("小明 麻辣香锅"),
	)
	if err != nil {
		t.Fatalf("insert memories_fts: %v", err)
	}

	q := BuildFTSQuery("想吃麻辣香锅")
	var gotID string
	err = s.readDB.QueryRowContext(
		ctx,
		`SELECT memory_id FROM memories_fts WHERE memories_fts MATCH ? LIMIT 1`,
		q,
	).Scan(&gotID)
	if err != nil {
		t.Fatalf("query memories_fts with %q: %v", q, err)
	}
	if gotID != "m1" {
		t.Fatalf("got memory_id = %q, want m1", gotID)
	}
}

func TestFloat32LERoundTrip(t *testing.T) {
	vec := make([]float32, 1024)
	for i := range vec {
		vec[i] = float32(i-512) * 0.001
	}
	raw := EncodeFloat32LE(vec)
	if len(raw) != 4096 {
		t.Fatalf("encoded length = %d, want 4096", len(raw))
	}
	got, err := DecodeFloat32LE(raw)
	if err != nil {
		t.Fatalf("DecodeFloat32LE: %v", err)
	}
	if !slices.Equal(got, vec) {
		t.Fatalf("decoded float32 slice mismatch")
	}
	if _, err := DecodeFloat32LE([]byte{1, 2, 3}); err == nil {
		t.Fatalf("expected error on misaligned byte slice")
	}
}

func TestWriteOnlySecretRedaction(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "secrets.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	cases := []struct {
		name     string
		key      string
		val      string
		wantHint string
		wantErr  bool
	}{
		{name: "long key", key: "gemini_api_key", val: "AIzaSySecretToken1234", wantHint: "***1234"},
		{name: "short key", key: "short_tok", val: "abcd1234", wantHint: "***"},
		{name: "empty key", key: "  ", val: "abc", wantErr: true},
		{name: "empty val", key: "bad_val", val: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.SetSecret(ctx, tc.key, tc.val)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("SetSecret: %v", err)
			}
		})
	}

	statuses, err := s.ListSecretStatus(ctx)
	if err != nil {
		t.Fatalf("ListSecretStatus: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("len(statuses) = %d, want 2", len(statuses))
	}

	rawJSON, err := json.Marshal(statuses)
	if err != nil {
		t.Fatalf("marshal statuses: %v", err)
	}
	if strings.Contains(string(rawJSON), "AIzaSySecretToken") || strings.Contains(string(rawJSON), "abcd1234") {
		t.Fatalf("ListSecretStatus leaked raw secret: %s", rawJSON)
	}

	byKey := map[string]SecretStatus{}
	for _, st := range statuses {
		byKey[st.Key] = st
	}
	if got := byKey["gemini_api_key"]; !got.Configured || got.MaskedHint != "***1234" {
		t.Fatalf("gemini_api_key status = %+v, want configured with ***1234", got)
	}
	if got := byKey["short_tok"]; !got.Configured || got.MaskedHint != "***" {
		t.Fatalf("short_tok status = %+v, want configured with ***", got)
	}

	resolved, ok, err := s.ResolveSecret(ctx, "gemini_api_key")
	if err != nil || !ok || resolved != "AIzaSySecretToken1234" {
		t.Fatalf("ResolveSecret = (%q, %v, %v)", resolved, ok, err)
	}

	if err := s.DeleteSecret(ctx, "short_tok"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	_, ok, err = s.ResolveSecret(ctx, "short_tok")
	if err != nil || ok {
		t.Fatalf("expected deleted secret to be absent, got ok=%v err=%v", ok, err)
	}
}
