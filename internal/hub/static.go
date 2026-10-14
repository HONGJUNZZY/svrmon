package hub

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

type staticAsset struct {
	data, gzip                  []byte
	contentType, etag, gzipETag string
}

// serveSPA caches embedded assets and their build-time gzip variants. Only
// fingerprinted assets are immutable; HTML revalidates on every navigation.
func serveSPA(distFS fs.FS) http.Handler {
	assets := make(map[string]staticAsset)
	fs.WalkDir(distFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".gz") {
			return nil
		}
		data, err := fs.ReadFile(distFS, p)
		if err != nil {
			return nil
		}
		compressed, _ := fs.ReadFile(distFS, p+".gz")
		contentType := mime.TypeByExtension(path.Ext(p))
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
		asset := staticAsset{data: data, gzip: compressed, contentType: contentType, etag: fmt.Sprintf(`"%x"`, sha256.Sum256(data))}
		if compressed != nil {
			asset.gzipETag = fmt.Sprintf(`"%x"`, sha256.Sum256(compressed))
		}
		assets[p] = asset
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		asset, ok := assets[p]
		if !ok && strings.HasPrefix(p, "assets/") {
			http.NotFound(w, r)
			return
		}
		if !ok {
			p = "index.html"
			asset, ok = assets[p]
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("Content-Type", asset.contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		data, etag := asset.data, asset.etag
		if asset.gzip != nil && acceptsGzip(r.Header.Get("Accept-Encoding")) {
			data, etag = asset.gzip, asset.gzipETag
			w.Header().Set("Content-Encoding", "gzip")
		}
		w.Header().Set("ETag", etag)
		http.ServeContent(w, r, p, time.Time{}, bytes.NewReader(data))
	})
}

func acceptsGzip(header string) bool {
	wildcard := false
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(item), ";")
		encoding := strings.ToLower(strings.TrimSpace(parts[0]))
		quality := 1.0
		for _, param := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(key, "q") {
				q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || q < 0 || q > 1 {
					quality = 0
				} else {
					quality = q
				}
			}
		}
		if encoding == "gzip" {
			return quality > 0
		}
		if encoding == "*" {
			wildcard = quality > 0
		}
	}
	return wildcard
}
