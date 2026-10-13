package httpsec

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newRouter(mw ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw...)
	r.Any("/api/*rest", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})
	r.NoRoute(func(c *gin.Context) { c.String(http.StatusOK, "ssr") })
	return r
}

func TestHeadersOnFallback(t *testing.T) {
	r := newRouter(Headers(true))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/browse", nil))
	if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing headers: %v", w.Header())
	}
	if w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("missing HSTS")
	}
	w = httptest.NewRecorder()
	newRouter(Headers(false)).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS without https")
	}
}

func TestBodyLimit(t *testing.T) {
	r := newRouter(BodyLimit(10, "/api/users/me/avatar"))
	big := strings.Repeat("x", 100)
	for path, want := range map[string]int{"/api/auth/login": 413, "/api/users/me/avatar": 200} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(big)))
		if w.Code != want {
			t.Errorf("%s: got %d want %d", path, w.Code, want)
		}
	}
}

func TestRateLimit(t *testing.T) {
	rules := []Rule{{Name: "reg", Method: http.MethodPost, Match: Exact("/api/auth/register"), Max: 2, Window: time.Minute}}
	r := newRouter(RateLimit(rules))
	codes := []int{}
	for range 3 {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/auth/register", nil))
		codes = append(codes, w.Code)
	}
	if codes[2] != http.StatusTooManyRequests || codes[1] != http.StatusOK {
		t.Fatalf("codes %v", codes)
	}
}

func TestSpoofedForwardedForIgnored(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rules := []Rule{{Name: "x", Match: Prefix("/api"), Max: 1, Window: time.Minute}}
	r := newRouter(RateLimit(rules))
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{200, 429} {
		req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
		req.Header.Set("X-Forwarded-For", []string{"1.1.1.1", "2.2.2.2"}[i])
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("request %d: got %d want %d", i, w.Code, want)
		}
	}
}

func TestOAuthStep(t *testing.T) {
	m := OAuthStep("start")
	if !m("/api/auth/github/start") || m("/api/auth/github/callback") || m("/api/auth/start") {
		t.Fatal("OAuthStep mismatch")
	}
}
