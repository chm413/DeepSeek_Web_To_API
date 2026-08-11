package webui

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

const welcomeHTML = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>DeepSeek_Web_To_API</title>
<style>body{font-family:Inter,system-ui,sans-serif;background:#030712;color:#f9fafb;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}a{color:#f59e0b;text-decoration:none}main{max-width:700px;padding:24px;text-align:center}h1{font-size:48px;margin:0 0 12px}.links{display:flex;gap:16px;justify-content:center;margin-top:20px;flex-wrap:wrap}</style>
</head><body><main><h1>DeepSeek_Web_To_API</h1><p>DeepSeek to OpenAI & Claude Compatible API</p><div class="links"><a href="/admin">管理面板</a><a href="/v1/models">API 状态</a><a href="https://github.com/chm413/DeepSeek_Web_To_API" target="_blank" rel="noreferrer noopener">GitHub</a></div></main></body></html>`

type Handler struct {
	StaticDir string
}

func NewHandler(staticDir string) *Handler {
	return &Handler{StaticDir: resolveStaticAdminDir(staticDir)}
}

func RegisterRoutes(r chi.Router, h *Handler) {
	r.Get("/", h.index)
	r.Get("/admin", h.admin)
}

func (h *Handler) HandleAdminFallback(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if !strings.HasPrefix(r.URL.Path, "/admin/") {
		return false
	}
	h.admin(w, r)
	return true
}

func (h *Handler) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(welcomeHTML))
}

func (h *Handler) admin(w http.ResponseWriter, r *http.Request) {
	staticDir := resolveStaticAdminDir(h.StaticDir)
	if fi, err := os.Stat(staticDir); err == nil && fi.IsDir() {
		h.serveFromDisk(w, r, staticDir)
		return
	}
	http.Error(w, "WebUI not built. Run `cd webui && npm run build` first.", http.StatusNotFound)
}

func (h *Handler) serveFromDisk(w http.ResponseWriter, r *http.Request, staticDir string) {
	path := strings.TrimPrefix(r.URL.Path, "/admin")
	path = strings.TrimPrefix(path, "/")
	if path != "" && strings.Contains(path, ".") {
		full, err := resolveAdminFilePath(staticDir, path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(full); err == nil {
			if strings.HasPrefix(path, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-store, must-revalidate")
			}
			// Pin Content-Type by extension before delegating to
			// http.ServeFile. On Windows, http.ServeFile consults
			// the HKEY_CLASSES_ROOT registry for MIME mappings;
			// third-party software is known to corrupt entries
			// (e.g. .css → application/xml), which silently breaks
			// the admin panel stylesheet. Setting Content-Type
			// here makes ServeFile skip its own DetectContentType
			// path. CJackHwang/ds2api 7870a61b.
			setExplicitContentType(w, path)
			http.ServeFile(w, r, full)
			return
		}
		http.NotFound(w, r)
		return
	}
	index := filepath.Join(staticDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	http.ServeFile(w, r, index)
}

// setExplicitContentType sets the Content-Type response header based on
// file extension. Hardcoded to bypass http.ServeFile's mime.TypeByExtension
// fallback which on Windows reads HKEY_CLASSES_ROOT and is known to be
// corruptible by third-party software (most commonly .css → application/xml,
// which breaks the admin panel stylesheet). Returns silently for unknown
// extensions, leaving ServeFile's DetectContentType path intact.
func setExplicitContentType(w http.ResponseWriter, path string) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js", ".mjs":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case ".json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	case ".html", ".htm":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".webp":
		w.Header().Set("Content-Type", "image/webp")
	case ".woff":
		w.Header().Set("Content-Type", "font/woff")
	case ".woff2":
		w.Header().Set("Content-Type", "font/woff2")
	case ".map":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	case ".ico":
		w.Header().Set("Content-Type", "image/x-icon")
	}
}

func resolveAdminFilePath(staticDir, requestPath string) (string, error) {
	base := strings.TrimSpace(staticDir)
	requestPath = strings.TrimSpace(requestPath)
	if base == "" || requestPath == "" {
		return "", errors.New("invalid path")
	}
	baseAbs, err := filepath.Abs(filepath.Clean(base))
	if err != nil {
		return "", err
	}
	fullPath := filepath.Clean(filepath.Join(baseAbs, filepath.FromSlash(requestPath)))
	rel, err := filepath.Rel(baseAbs, fullPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path traversal detected")
	}
	return fullPath, nil
}

func resolveStaticAdminDir(preferred string) string {
	if strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR")) != "" {
		return filepath.Clean(preferred)
	}
	candidates := []string{preferred}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "static/admin"))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "static/admin"),
			filepath.Join(filepath.Dir(exeDir), "static/admin"),
		)
	}
	// Common serverless locations.
	candidates = append(candidates, "/var/task/static/admin", "/var/task/user/static/admin")

	seen := map[string]struct{}{}
	for _, c := range candidates {
		c = filepath.Clean(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return filepath.Clean(preferred)
}
