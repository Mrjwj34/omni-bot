package webui

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestNewHandler(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":                  {Data: []byte("<!doctype html><html><body>omni-bot-spa</body></html>")},
		"favicon.svg":                 {Data: []byte("<svg></svg>")},
		"_app/version.json":           {Data: []byte(`{"version":"1"}`)},
		"_app/immutable/entry/app.js": {Data: []byte("console.log('omni-bot');")},
		".gitkeep":                    {Data: []byte("")},
		"README.md":                   {Data: []byte("# internal")},
	}

	srv := httptest.NewServer(NewHandler(assets))
	t.Cleanup(srv.Close)

	tests := []struct {
		name           string
		method         string
		path           string
		wantStatus     int
		wantBodySubstr string
		wantCacheCtrl  string
		wantCTPrefix   string
	}{
		{
			name:           "root serves index.html",
			method:         http.MethodGet,
			path:           "/",
			wantStatus:     http.StatusOK,
			wantBodySubstr: "omni-bot-spa",
			wantCacheCtrl:  "no-cache",
			wantCTPrefix:   "text/html",
		},
		{
			name:           "client route /memory falls back to index.html",
			method:         http.MethodGet,
			path:           "/memory",
			wantStatus:     http.StatusOK,
			wantBodySubstr: "omni-bot-spa",
			wantCacheCtrl:  "no-cache",
			wantCTPrefix:   "text/html",
		},
		{
			name:           "nested client route falls back to index.html",
			method:         http.MethodGet,
			path:           "/traces/run-42",
			wantStatus:     http.StatusOK,
			wantBodySubstr: "omni-bot-spa",
			wantCacheCtrl:  "no-cache",
			wantCTPrefix:   "text/html",
		},
		{
			name:           "immutable asset sets long-lived cache header",
			method:         http.MethodGet,
			path:           "/_app/immutable/entry/app.js",
			wantStatus:     http.StatusOK,
			wantBodySubstr: "console.log('omni-bot');",
			wantCacheCtrl:  "public, max-age=31536000, immutable",
		},
		{
			name:           "static favicon sets standard cache header",
			method:         http.MethodGet,
			path:           "/favicon.svg",
			wantStatus:     http.StatusOK,
			wantBodySubstr: "<svg>",
			wantCacheCtrl:  "public, max-age=3600",
		},
		{
			name:          "version json disables caching",
			method:        http.MethodGet,
			path:          "/_app/version.json",
			wantStatus:    http.StatusOK,
			wantCacheCtrl: "no-cache",
		},
		{
			name:       "missing asset with extension returns 404",
			method:     http.MethodGet,
			path:       "/_app/immutable/missing.js",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "reserved api route returns 404 instead of SPA HTML",
			method:     http.MethodGet,
			path:       "/api/unknown",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "reserved ws route returns 404",
			method:     http.MethodGet,
			path:       "/ws",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "internal gitkeep is blocked",
			method:     http.MethodGet,
			path:       "/.gitkeep",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "internal README is blocked",
			method:     http.MethodGet,
			path:       "/README.md",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "HEAD request succeeds on SPA route",
			method:     http.MethodHead,
			path:       "/persona",
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST method rejected with 405",
			method:     http.MethodPost,
			path:       "/memory",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	client := srv.Client()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tc.method, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantCacheCtrl != "" && resp.Header.Get("Cache-Control") != tc.wantCacheCtrl {
				t.Fatalf("Cache-Control = %q, want %q", resp.Header.Get("Cache-Control"), tc.wantCacheCtrl)
			}
			if tc.wantCTPrefix != "" && !strings.HasPrefix(resp.Header.Get("Content-Type"), tc.wantCTPrefix) {
				t.Fatalf("Content-Type = %q, want prefix %q", resp.Header.Get("Content-Type"), tc.wantCTPrefix)
			}
			if tc.wantBodySubstr != "" {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				if !strings.Contains(string(body), tc.wantBodySubstr) {
					t.Fatalf("body = %q, want substring %q", string(body), tc.wantBodySubstr)
				}
			}
		})
	}
}

func TestNewHandlerMissingIndexReturnsServiceUnavailable(t *testing.T) {
	assets := fstest.MapFS{
		".gitkeep": {Data: []byte("")},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	NewHandler(assets).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestEmbeddedHandler(t *testing.T) {
	_, err := fs.Stat(Assets(), "index.html")
	hasBuiltSPA := err == nil

	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/memory", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if hasBuiltSPA {
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("built SPA /memory status = %d, want 200", resp.StatusCode)
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("built SPA Content-Type = %q, want text/html", resp.Header.Get("Content-Type"))
		}
	} else if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unbuilt SPA /memory status = %d, want 503", resp.StatusCode)
	}
}
