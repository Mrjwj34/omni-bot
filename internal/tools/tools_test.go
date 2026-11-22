package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"omni-bot/internal/ai"
	"omni-bot/internal/meme"
	"omni-bot/internal/store"
)

func TestRegistrySafeExecutionAndDeterminism(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register(FuncTool{
		Def: ai.ToolDef{Name: "z_tool", Description: "z"},
		Fn: func(_ context.Context, _ []byte) (Result, error) {
			return Result{Text: "ok-z"}, nil
		},
	}); err != nil {
		t.Fatalf("register z_tool: %v", err)
	}
	if err := reg.Register(FuncTool{
		Def: ai.ToolDef{Name: "a_panic", Description: "panics"},
		Fn: func(_ context.Context, _ []byte) (Result, error) {
			panic("boom")
		},
	}); err != nil {
		t.Fatalf("register a_panic: %v", err)
	}
	if err := reg.Register(FuncTool{
		Def: ai.ToolDef{Name: "m_err", Description: "returns err"},
		Fn: func(_ context.Context, _ []byte) (Result, error) {
			return Result{}, errors.New("upstream failed")
		},
	}); err != nil {
		t.Fatalf("register m_err: %v", err)
	}

	// Duplicate registration must fail.
	if err := reg.Register(FuncTool{Def: ai.ToolDef{Name: "z_tool"}}); err == nil {
		t.Fatalf("expected error on duplicate tool registration")
	}

	// ToolDefs must be deterministically sorted by name.
	defs := reg.ToolDefs()
	if len(defs) != 3 || defs[0].Name != "a_panic" || defs[1].Name != "m_err" || defs[2].Name != "z_tool" {
		t.Fatalf("unexpected ToolDefs order: %+v", defs)
	}

	cases := []struct {
		name      string
		tool      string
		args      string
		wantErr   bool
		wantSubst string
	}{
		{"ok", "z_tool", `{}`, false, "ok-z"},
		{"empty_args_defaults_object", "z_tool", ``, false, "ok-z"},
		{"unknown_tool", "missing", `{}`, true, "unknown tool"},
		{"invalid_json", "z_tool", `{bad`, true, "invalid JSON"},
		{"panic_recovered", "a_panic", `{}`, true, "panicked: boom"},
		{"error_converted", "m_err", `{}`, true, "upstream failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := reg.Execute(t.Context(), tc.tool, []byte(tc.args))
			if res.IsError != tc.wantErr {
				t.Fatalf("IsError=%v, want %v (text=%q)", res.IsError, tc.wantErr, res.Text)
			}
			if !strings.Contains(res.Text, tc.wantSubst) {
				t.Fatalf("expected %q in %q", tc.wantSubst, res.Text)
			}
		})
	}

	// Snapshot must isolate from subsequent mutations.
	snap := reg.Snapshot()
	reg.Unregister("z_tool")
	if _, ok := snap.Get("z_tool"); !ok {
		t.Fatalf("snapshot lost z_tool after parent unregister")
	}
}

type stubRecaller struct {
	lastQuery MemoryRecallQuery
	items     []RecalledMemory
}

func (s *stubRecaller) Recall(_ context.Context, q MemoryRecallQuery) ([]RecalledMemory, error) {
	s.lastQuery = q
	return s.items, nil
}

func TestMemoryRecallTool(t *testing.T) {
	recaller := &stubRecaller{
		items: []RecalledMemory{
			{
				ID:        "mem-1",
				Kind:      "event",
				Content:   "老王上周去成都吃了火锅",
				TimeEvent: "2026-10-12",
				Entities:  []string{"老王", "成都"},
				Score:     0.91,
			},
			{
				ID:        "mem-2",
				Kind:      "fact",
				Content:   "老王之前养过一只橘猫",
				TimeEvent: "2025-05-01",
				Archived:  true,
			},
		},
	}

	reg := NewRegistry()
	if err := reg.Register(NewMemoryRecallTool(recaller)); err != nil {
		t.Fatalf("register memory_recall: %v", err)
	}

	// Missing all filters should return error result.
	badRes := reg.Execute(t.Context(), "memory_recall", []byte(`{}`))
	if !badRes.IsError {
		t.Fatalf("expected error for empty memory_recall query")
	}

	res := reg.Execute(t.Context(), "memory_recall", []byte(`{
		"query": "老王 成都",
		"entity": "老王",
		"time_from": "2025-01-01",
		"time_to": "2026-12-31",
		"include_archived": true,
		"limit": 99
	}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if recaller.lastQuery.Limit != 25 || !recaller.lastQuery.IncludeArchived || recaller.lastQuery.Entity != "老王" {
		t.Fatalf("unexpected forwarded query: %+v", recaller.lastQuery)
	}
	if !strings.Contains(res.Text, "[2026-10-12]") || !strings.Contains(res.Text, "[archived]") {
		t.Fatalf("unexpected recall text:\n%s", res.Text)
	}
}

func TestWebSearchToolWithHTTPTest(t *testing.T) {
	var gotAuth string
	var gotPath string
	var gotPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{
					"title":        "Go 1.26 Release Notes",
					"url":          "https://go.dev/doc/go1.26",
					"snippet":      "Latest Go standard library improvements.",
					"published_at": "2026-02-15",
				},
			},
		})
	}))
	defer srv.Close()

	reg := NewRegistry()
	if err := reg.Register(NewWebSearchTool(WebSearchConfig{
		BaseURL: srv.URL,
		APIKey:  "ds-secret-token",
	})); err != nil {
		t.Fatalf("register web_search: %v", err)
	}

	res := reg.Execute(t.Context(), "web_search", []byte(`{"query":"Go 1.26","max_results":3}`))
	if res.IsError {
		t.Fatalf("web_search failed: %s", res.Text)
	}
	if gotPath != "/v1/tools/search" || gotAuth != "Bearer ds-secret-token" {
		t.Fatalf("unexpected upstream request: path=%q auth=%q", gotPath, gotAuth)
	}
	if gotPayload["query"] != "Go 1.26" {
		t.Fatalf("unexpected payload: %+v", gotPayload)
	}
	if !strings.Contains(res.Text, "Go 1.26 Release Notes") || !strings.Contains(res.Text, "https://go.dev/doc/go1.26") {
		t.Fatalf("unexpected web_search output:\n%s", res.Text)
	}
}

func TestWebFetchSSRFGuardAndHTMLExtraction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
<head>
  <title>Test &amp; Demo Page</title>
  <style>body { color: red; } .hidden { display: none; }</style>
</head>
<body>
  <nav><a href="/home">NavHome</a><a href="/login">NavLogin</a></nav>
  <main>
    <h1>Hello &lt;World&gt;</h1>
    <p>This is the   main   article body.</p>
    <script>var secret = "DO_NOT_SHOW_SCRIPT"; if (1 < 2) { alert(secret); }</script>
    <p>Second paragraph with &quot;quotes&quot;.</p>
  </main>
  <footer>Copyright 2026</footer>
</body>
</html>`))
	}))
	defer srv.Close()

	// Default config MUST block loopback / private addresses (SSRF guard).
	regStrict := NewRegistry()
	_ = regStrict.Register(NewWebFetchTool(WebFetchConfig{}))

	for _, blockedURL := range []string{
		srv.URL,
		"http://127.0.0.1:8080/secret",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/internal",
		"http://localhost/admin",
		"file:///etc/passwd",
	} {
		res := regStrict.ExecuteMap(t.Context(), "web_fetch", map[string]any{"url": blockedURL})
		if !res.IsError {
			t.Fatalf("expected SSRF/scheme block for %q, got success: %s", blockedURL, res.Text)
		}
	}

	// With AllowPrivateNetworks enabled for httptest, HTML extraction strips script/style/nav and decodes entities.
	regTest := NewRegistry()
	_ = regTest.Register(NewWebFetchTool(WebFetchConfig{
		AllowPrivateNetworks: true,
		MaxOutputChars:       500,
	}))

	res := regTest.ExecuteMap(t.Context(), "web_fetch", map[string]any{"url": srv.URL})
	if res.IsError {
		t.Fatalf("web_fetch failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Title: Test & Demo Page") {
		t.Fatalf("expected decoded title, got:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "Hello <World>") || !strings.Contains(res.Text, `Second paragraph with "quotes".`) {
		t.Fatalf("expected extracted main text, got:\n%s", res.Text)
	}
	for _, unwanted := range []string{"DO_NOT_SHOW_SCRIPT", "color: red", "NavHome", "NavLogin"} {
		if strings.Contains(res.Text, unwanted) {
			t.Fatalf("unwanted boilerplate %q leaked into extracted text:\n%s", unwanted, res.Text)
		}
	}

	// Verify truncation honors max_chars cleanly.
	truncRes := regTest.ExecuteMap(t.Context(), "web_fetch", map[string]any{"url": srv.URL, "max_chars": 25})
	if truncRes.IsError || !strings.Contains(truncRes.Text, "[truncated]") {
		t.Fatalf("expected truncated output, got err=%v text=%q", truncRes.IsError, truncRes.Text)
	}
}

func TestManageMemeToolWithRealVault(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(t.Context(), filepath.Join(dir, "memes.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	blobs, err := store.NewBlobStore(filepath.Join(dir, "media"))
	if err != nil {
		t.Fatalf("new blob store: %v", err)
	}
	vault, err := meme.NewVault(meme.Options{Store: st, Blobs: blobs})
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}

	// Create a real PNG image and ingest it.
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := range 40 {
		for x := range 40 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 6), B: 180, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	ingested, err := vault.Ingest(t.Context(), buf.Bytes(), ".png", "捂脸社死小猫")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	shortID := "#" + meme.ShortHash(ingested.Meme.Hash)

	reg := NewRegistry()
	if err := reg.Register(NewManageMemeTool(vault)); err != nil {
		t.Fatalf("register manage_meme: %v", err)
	}

	// 1. Search
	searchRes := reg.ExecuteMap(t.Context(), "manage_meme", map[string]any{
		"action": "search",
		"query":  "社死",
	})
	if searchRes.IsError || !strings.Contains(searchRes.Text, shortID) {
		t.Fatalf("search failed: err=%v text=%q", searchRes.IsError, searchRes.Text)
	}

	// 2. Update impression
	updRes := reg.ExecuteMap(t.Context(), "manage_meme", map[string]any{
		"action":      "update_impression",
		"hash":        shortID,
		"description": "双手捂脸尴尬到裂开的猫",
		"emotions":    "尴尬, 社死",
	})
	if updRes.IsError || !strings.Contains(updRes.Text, "裂开的猫") {
		t.Fatalf("update_impression failed: err=%v text=%q", updRes.IsError, updRes.Text)
	}

	// 3. Delete (soft delete + blacklist)
	delRes := reg.ExecuteMap(t.Context(), "manage_meme", map[string]any{
		"action": "delete",
		"hash":   shortID,
	})
	if delRes.IsError || !strings.Contains(delRes.Text, "blacklisted") {
		t.Fatalf("delete failed: err=%v text=%q", delRes.IsError, delRes.Text)
	}

	// 4. Re-search should return empty and re-ingest must be blacklisted.
	afterSearch := reg.ExecuteMap(t.Context(), "manage_meme", map[string]any{
		"action": "search",
		"query":  "裂开的猫",
	})
	if afterSearch.IsError || !strings.Contains(afterSearch.Text, "No matching memes") {
		t.Fatalf("expected no matching memes after delete, got %q", afterSearch.Text)
	}
	reingest, err := vault.Ingest(t.Context(), buf.Bytes(), ".png", "again")
	if err != nil || reingest.Status != meme.IngestStatusBlacklisted {
		t.Fatalf("expected blacklisted after manage_meme delete, got status=%q err=%v", reingest.Status, err)
	}
}
