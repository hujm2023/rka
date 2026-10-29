package httpx

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".md":    "text/markdown; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".webp":  "image/webp",
	".gif":   "image/gif",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".map":   "application/json; charset=utf-8",
	".wasm":  "application/wasm",
	".pdf":   "application/pdf",
}

// Static serves the built browser application from dir, falling back to
// index.html for client-side routes. Missing files with an extension are 404s,
// so a broken asset never turns into HTML.
func Static(dir string) http.Handler {
	root, err := filepath.Abs(dir)
	if err != nil {
		root = dir
	}
	index := filepath.Join(root, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			MethodNotAllowed(w, r, http.MethodGet, http.MethodHead)
			return
		}
		if _, err := os.Stat(index); err != nil {
			Fail(w, r, http.StatusServiceUnavailable, "no_frontend", "前端构建产物缺失：请先运行 npm run build，或设置 RKA_STATIC_DIR。")
			return
		}
		requested := path.Clean("/" + r.URL.Path)
		// Reject NUL and any path that tries to escape the root.
		if strings.ContainsRune(requested, 0) {
			Fail(w, r, http.StatusBadRequest, "bad_path", "路径不合法。")
			return
		}
		file := filepath.Join(root, filepath.FromSlash(requested))
		if file != root && !strings.HasPrefix(file, root+string(os.PathSeparator)) {
			Fail(w, r, http.StatusForbidden, "bad_path", "路径不合法。")
			return
		}
		info, statErr := os.Stat(file)
		if statErr != nil || info.IsDir() {
			if path.Ext(requested) != "" {
				http.NotFound(w, r)
				return
			}
			serveFile(w, r, index)
			return
		}
		serveFile(w, r, file)
	})
}

func serveFile(w http.ResponseWriter, r *http.Request, file string) {
	ext := strings.ToLower(filepath.Ext(file))
	contentType := contentTypes[ext]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	switch {
	case strings.Contains(file, string(os.PathSeparator)+"assets"+string(os.PathSeparator)):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case ext == ".html":
		w.Header().Set("Cache-Control", "no-cache")
	default:
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	http.ServeFile(w, r, file)
}
