package hub

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func staticFixture(t *testing.T) (http.Handler, []byte) {
	t.Helper()
	js := []byte("console.log('monitor');")
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	gz.Write(js)
	gz.Close()
	return serveSPA(fstest.MapFS{
		"index.html":                {Data: []byte("<!doctype html><title>svrmon</title>")},
		"assets/app-abcd1234.js":    {Data: js},
		"assets/app-abcd1234.js.gz": {Data: compressed.Bytes()},
	}), js
}

func TestStaticGzipAndValidation(t *testing.T) {
	handler, js := staticFixture(t)
	req := httptest.NewRequest("GET", "/assets/app-abcd1234.js", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 200 || res.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip response: %d %v", res.Code, res.Header())
	}
	if !strings.Contains(res.Header().Get("Content-Type"), "javascript") || res.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatal("wrong gzip metadata")
	}
	gz, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(gz)
	gz.Close()
	if err != nil || !bytes.Equal(got, js) {
		t.Fatal("gzip payload corrupted")
	}
	if res.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatal("asset should be immutable")
	}
	req = httptest.NewRequest("GET", "/assets/app-abcd1234.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", res.Header().Get("ETag"))
	cached := httptest.NewRecorder()
	handler.ServeHTTP(cached, req)
	if cached.Code != 304 || cached.Body.Len() != 0 {
		t.Fatal("conditional gzip request should be 304")
	}
}

func TestStaticFallbackAndIdentity(t *testing.T) {
	handler, js := staticFixture(t)
	for _, enc := range []string{"", "gzip;q=0", "gzip;q=0, *;q=1"} {
		req := httptest.NewRequest("GET", "/assets/app-abcd1234.js", nil)
		req.Header.Set("Accept-Encoding", enc)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 200 || res.Header().Get("Content-Encoding") != "" || !bytes.Equal(res.Body.Bytes(), js) {
			t.Fatalf("identity response failed for %q", enc)
		}
	}
	for _, p := range []string{"/", "/index.html", "/s/server-003"} {
		req := httptest.NewRequest("GET", p, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 200 || res.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("HTML route %s failed", p)
		}
		req.Header.Set("If-None-Match", res.Header().Get("ETag"))
		cached := httptest.NewRecorder()
		handler.ServeHTTP(cached, req)
		if cached.Code != 304 {
			t.Fatal("HTML should revalidate")
		}
	}
	req := httptest.NewRequest("GET", "/assets/missing.js", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 404 {
		t.Fatal("missing script should not return SPA HTML")
	}
	req = httptest.NewRequest("HEAD", "/assets/app-abcd1234.js", nil)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 200 || res.Body.Len() != 0 {
		t.Fatal("HEAD must not send a body")
	}
}

func TestAcceptsGzip(t *testing.T) {
	for input, want := range map[string]bool{"gzip": true, "br, gzip;q=0.5": true, "*;q=1": true, "gzip;q=0": false, "*;q=1, gzip;q=0": false, "gzip;q=invalid": false, "": false} {
		if got := acceptsGzip(input); got != want {
			t.Fatalf("acceptsGzip(%q)=%v, want %v", input, got, want)
		}
	}
}
