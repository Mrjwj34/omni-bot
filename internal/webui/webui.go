package webui

import (
	"bytes"
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var embeddedAssets embed.FS

var blockedInternalFiles = map[string]bool{
	".gitkeep":  true,
	"README.md": true,
}

func Assets() fs.FS {
	sub, err := fs.Sub(embeddedAssets, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Handler serves the embedded SvelteKit SPA with client-side route fallback to index.html.
func Handler() http.Handler {
	return NewHandler(Assets())
}

// NewHandler serves a static SPA filesystem with immutable asset caching and index.html fallback.
func NewHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		cleanPath := path.Clean("/" + r.URL.Path)
		if isReservedBackendPath(cleanPath) {
			http.NotFound(w, r)
			return
		}

		rel := strings.TrimPrefix(cleanPath, "/")
		if rel != "" {
			if isBlockedInternal(rel) {
				http.NotFound(w, r)
				return
			}
			if served := tryServeFile(w, r, assets, rel); served {
				return
			}
			// Missing files with an explicit extension should 404 rather than return SPA HTML.
			if strings.Contains(path.Base(rel), ".") {
				http.NotFound(w, r)
				return
			}
		}

		if served := tryServeFile(w, r, assets, "index.html"); served {
			return
		}
		http.Error(w, "WebUI assets not built; run pnpm --dir web build", http.StatusServiceUnavailable)
	})
}

func tryServeFile(w http.ResponseWriter, r *http.Request, assets fs.FS, name string) bool {
	f, err := assets.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}

	w.Header().Set("Cache-Control", cacheControlFor(name))
	if name == "index.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}

	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, info.Name(), info.ModTime(), rs)
		return true
	}

	data, err := io.ReadAll(f)
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(data))
	return true
}

func cacheControlFor(rel string) string {
	switch {
	case strings.HasPrefix(rel, "_app/immutable/"):
		return "public, max-age=31536000, immutable"
	case rel == "index.html" || rel == "_app/version.json":
		return "no-cache"
	default:
		return "public, max-age=3600"
	}
}

func isReservedBackendPath(cleanPath string) bool {
	for _, prefix := range []string{"/api", "/ws", "/media"} {
		if cleanPath == prefix || strings.HasPrefix(cleanPath, prefix+"/") {
			return true
		}
	}
	return false
}

func isBlockedInternal(rel string) bool {
	return blockedInternalFiles[rel] || blockedInternalFiles[path.Base(rel)]
}
