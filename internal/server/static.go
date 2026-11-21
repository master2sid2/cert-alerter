package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

type asset struct {
	data        []byte
	gz          []byte
	etag        string
	contentType string
}

type assetStore struct {
	files map[string]*asset
}

var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".svg":         "image/svg+xml",
	".json":        "application/json",
	".png":         "image/png",
	".ico":         "image/x-icon",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

// loadAssets reads all embedded files into memory, pre-compressing text.
func loadAssets(fsys fs.FS) (*assetStore, error) {
	st := &assetStore{files: map[string]*asset{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		ext := strings.ToLower(path.Ext(p))
		ct := contentTypes[ext]
		if ct == "" {
			ct = mime.TypeByExtension(ext)
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		sum := sha256.Sum256(data)
		a := &asset{data: data, contentType: ct, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		if len(data) > 512 && (strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "svg")) {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			_, _ = zw.Write(data)
			_ = zw.Close()
			if buf.Len() < len(data) {
				a.gz = buf.Bytes()
			}
		}
		st.files[p] = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	if st.files["index.html"] == nil {
		return nil, fs.ErrNotExist
	}
	return st, nil
}

// staticHandler serves /assets/* from the embedded files and index.html for
// every other path so the single page application can route client side.
func (s *Server) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := r.URL.Path
		assets := s.assets
		if s.devStatic {
			if fresh, err := loadAssets(s.Static); err == nil {
				assets = fresh
			}
		}
		var a *asset
		switch {
		case strings.HasPrefix(p, "/assets/"):
			a = assets.files[strings.TrimPrefix(path.Clean(p), "/assets/")]
			if a == nil {
				http.NotFound(w, r)
				return
			}
		case p == "/favicon.svg" || p == "/favicon.ico":
			a = assets.files["favicon.svg"]
		default:
			a = assets.files["index.html"]
		}
		if a == nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", a.contentType)
		h.Set("ETag", a.etag)
		h.Set("Cache-Control", "no-cache")
		h.Set("Vary", "Accept-Encoding")
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, a.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		body := a.data
		if a.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			body = a.gz
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}
