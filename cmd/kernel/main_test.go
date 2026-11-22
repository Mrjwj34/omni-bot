package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	_ "modernc.org/sqlite"

	"omni-bot/internal/store"
	"omni-bot/internal/webui"
)

func TestCLI_MigrateLegacy_ReadOnlyAndIdempotent(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Create minimal legacy timeline.db fixture inside srcDir.
	tlPath := filepath.Join(srcDir, "timeline.db")
	db, err := sql.Open("sqlite", tlPath)
	if err != nil {
		t.Fatalf("open fixture timeline.db: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE group_timeline (
			seq_id INTEGER PRIMARY KEY,
			group_id TEXT,
			msg_id TEXT,
			sender_id TEXT,
			sender_name TEXT,
			is_bot INTEGER,
			is_at_bot INTEGER,
			timestamp REAL,
			rendered_text TEXT,
			image_paths_json TEXT,
			audio_paths_json TEXT,
			status TEXT,
			archived_to_memory INTEGER,
			media_refs_json TEXT
		);
		INSERT INTO group_timeline VALUES (
			1, 'g-100', 'm-1', 'u-1', '测试群友', 0, 0, 1710000000.0, '旧版测试消息', '[]', '[]', 'PROCESSED', 0, '[]'
		);
	`)
	_ = db.Close()
	if err != nil {
		t.Fatalf("seed fixture timeline.db: %v", err)
	}

	beforeStat, err := os.Stat(tlPath)
	if err != nil {
		t.Fatalf("stat source timeline.db before: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"migrate-legacy", "--from", srcDir, "--data", dstDir}, &stdout, &stderr); err != nil {
		t.Fatalf("migrate-legacy #1: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Timeline messages : 1") {
		t.Fatalf("unexpected migration output:\n%s", stdout.String())
	}

	// Verify source file was NOT modified.
	afterStat, err := os.Stat(tlPath)
	if err != nil {
		t.Fatalf("stat source timeline.db after: %v", err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) || beforeStat.Size() != afterStat.Size() {
		t.Fatalf("source timeline.db appears modified: before=%v/%d after=%v/%d",
			beforeStat.ModTime(), beforeStat.Size(), afterStat.ModTime(), afterStat.Size())
	}

	// Re-run migrate-legacy to confirm idempotency.
	stdout.Reset()
	if err := run(t.Context(), []string{"migrate-legacy", "--from", srcDir, "--data", dstDir}, &stdout, &stderr); err != nil {
		t.Fatalf("migrate-legacy #2: %v", err)
	}

	st, err := store.Open(t.Context(), filepath.Join(dstDir, "omni.db"))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = st.Close() }()

	msgs, err := st.ListRecentTimeline(t.Context(), "g-100", 10)
	if err != nil {
		t.Fatalf("ListRecentTimeline: %v", err)
	}
	if len(msgs) != 1 || msgs[0].RenderedText != "旧版测试消息" {
		t.Fatalf("unexpected migrated timeline: %+v", msgs)
	}
}

func TestCLI_ServeLiveHTTP_APIAndWebUI(t *testing.T) {
	dataDir := t.TempDir()
	srvCtx, cancel := context.WithCancel(t.Context())
	defer cancel()

	readyCh := make(chan string, 1)
	errCh := make(chan error, 1)

	spaFS := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><html><body>omni-bot-webui</body></html>")},
	}

	var stdout, stderr bytes.Buffer
	go func() {
		errCh <- runServe(srvCtx, []string{
			"--addr", "127.0.0.1:0",
			"--data", dataDir,
			"--log-level", "warn",
		}, &stdout, &stderr, webui.NewHandler(spaFS), func(addr string) {
			readyCh <- addr
		})
	}()

	var addr string
	select {
	case addr = <-readyCh:
	case err := <-errCh:
		t.Fatalf("server exited early: %v (stderr=%s)", err, stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for kernel HTTP server to listen")
	}

	baseURL := "http://" + addr

	// 1. Verify /api/dashboard returns 200 OK JSON.
	apiRes, err := http.Get(baseURL + "/api/dashboard")
	if err != nil {
		t.Fatalf("GET /api/dashboard: %v", err)
	}
	apiBody, _ := io.ReadAll(apiRes.Body)
	_ = apiRes.Body.Close()
	if apiRes.StatusCode != http.StatusOK || !strings.Contains(string(apiBody), `"status":"ok"`) {
		t.Fatalf("GET /api/dashboard: status=%d body=%s", apiRes.StatusCode, string(apiBody))
	}

	// 2. Verify / serves embedded WebUI SPA HTML.
	uiRes, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	uiBody, _ := io.ReadAll(uiRes.Body)
	_ = uiRes.Body.Close()
	if uiRes.StatusCode != http.StatusOK || !strings.Contains(string(uiBody), "omni-bot-webui") {
		t.Fatalf("GET /: status=%d body=%s", uiRes.StatusCode, string(uiBody))
	}

	// 3. Verify graceful shutdown on context cancellation.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server shutdown returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for graceful shutdown")
	}
}
