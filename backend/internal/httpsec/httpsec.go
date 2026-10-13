// Package httpsec holds the cross-cutting HTTP hardening for Hypertube:
// security headers, request body limits, per-IP rate limits and the
// trusted-proxy configuration that makes c.ClientIP() trustworthy.
package httpsec

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ContentSecurityPolicy is sent on every response, including the ones the Go
// server proxies from the Angular SSR server.
//
//   - script-src 'self': only the hashed bundles. 'unsafe-hashes' + the two
//     hashes allow exactly the onload="this.media='all'" handler that Angular's
//     critical-CSS inlining puts on the stylesheet <link> in production builds
//     (without it styles.css would stay media="print"). The inline
//     event-replay bootstrap script emitted by withEventReplay() is blocked:
//     hydration still works, only clicks made before hydration are not replayed.
//   - style-src 'unsafe-inline': SSR inlines component styles and critical CSS
//     as <style> tags; nonces would need a per-request nonce threaded into the
//     SSR render (ngCspNonce), which is out of scope.
//   - img-src https:: posters (archive.org, OMDb/m.media-amazon.com, ...) and
//     OAuth avatars (GitHub, 42 CDN) come from many https hosts.
//   - media-src 'self' blob:: the player streams from /stream (and subtitles).
//   - static.cloudflareinsights.com / cloudflareinsights.com: the Cloudflare
//     Web Analytics beacon the proxy injects into every HTML page.
const ContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-hashes' 'sha256-MhtPZXr7+LpJUY5qtMutB+qWfQtMaPccfe7QXtCcEYc=' 'sha256-kx8Md/dUVxiWxH0xNDfI+0jY+S4Ev2Jri3jBG4rrm9E=' https://static.cloudflareinsights.com; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' https://fonts.gstatic.com; " +
	"img-src 'self' data: blob: https:; " +
	"media-src 'self' blob:; " +
	"connect-src 'self' https://cloudflareinsights.com; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// Headers sets the security headers. hsts adds Strict-Transport-Security and
// must only be true when the site is served over https (COOKIE_SECURE).
func Headers(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		// /stream serves video and subtitle files, not pages: opened directly,
		// the browser's own media viewer injects scripts the page CSP blocks.
		if !strings.HasPrefix(c.Request.URL.Path, "/stream/") {
			h.Set("Content-Security-Policy", ContentSecurityPolicy)
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// DefaultBodyLimit caps request bodies on /api (JSON). Routes listed in
// BodyLimit's exempt set enforce their own, larger, limit.
const DefaultBodyLimit = 1 << 20

// BodyLimit wraps request bodies under /api with http.MaxBytesReader(max),
// except for exempt paths (e.g. the avatar upload, which allows 5 MB itself).
func BodyLimit(max int64, exempt ...string) gin.HandlerFunc {
	skip := map[string]bool{}
	for _, p := range exempt {
		skip[p] = true
	}
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if c.Request.Body != nil && underAPI(p) && !skip[p] {
			if c.Request.ContentLength > max {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Request body too large"})
				return
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		}
		c.Next()
	}
}

func underAPI(p string) bool { return p == "/api" || strings.HasPrefix(p, "/api/") }

// Rule limits requests matching Method (empty = any) and Match per client IP
// to Max per Window.
type Rule struct {
	Name   string
	Method string
	Match  func(path string) bool
	Max    int
	Window time.Duration
}

// Prefix matches path and everything below it.
func Prefix(prefix string) func(string) bool {
	return func(p string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }
}

// Exact matches one path.
func Exact(path string) func(string) bool {
	return func(p string) bool { return p == path }
}

// OAuthStep matches /api/auth/<provider>/<step>.
func OAuthStep(step string) func(string) bool {
	return func(p string) bool {
		rest, ok := strings.CutPrefix(p, "/api/auth/")
		if !ok {
			return false
		}
		provider, s, ok := strings.Cut(rest, "/")
		return ok && provider != "" && s == step
	}
}

// DefaultRules are the per-IP limits applied in front of the routes. They
// complement the failure counters inside the login/forgot/reset handlers.
func DefaultRules() []Rule {
	return []Rule{
		{Name: "register", Method: http.MethodPost, Match: Exact("/api/auth/register"), Max: 10, Window: time.Hour},
		{Name: "oauth-signup", Method: http.MethodPost, Match: Exact("/api/auth/oauth/signup"), Max: 10, Window: time.Hour},
		{Name: "oauth-start", Method: http.MethodGet, Match: OAuthStep("start"), Max: 30, Window: 10 * time.Minute},
		{Name: "oauth-callback", Method: http.MethodGet, Match: OAuthStep("callback"), Max: 30, Window: 10 * time.Minute},
		{Name: "api-token", Method: http.MethodPost, Match: Exact("/api/v1/oauth/token"), Max: 30, Window: time.Minute},
		// Generous general limit: the SPA polls download progress and pages lists.
		{Name: "api", Match: Prefix("/api"), Max: 1200, Window: time.Minute},
	}
}

// RateLimit enforces every matching rule (a request can count against a
// specific rule and the general one). Over the limit it answers 429 with
// Retry-After. Counters are in memory, per process (single replica).
func RateLimit(rules []Rule) gin.HandlerFunc {
	limiters := make([]*counter, len(rules))
	for i, r := range rules {
		limiters[i] = newCounter(r.Max, r.Window)
	}
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		ip := c.ClientIP()
		for i, r := range rules {
			if (r.Method != "" && r.Method != c.Request.Method) || !r.Match(p) {
				continue
			}
			if wait := limiters[i].take(ip); wait > 0 {
				c.Header("Retry-After", fmt.Sprint(int(math.Ceil(wait.Seconds()))))
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests, try again later"})
				return
			}
		}
		c.Next()
	}
}

// counter is a fixed-window request counter per key.
type counter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	hits   map[string]*slot
}

type slot struct {
	count int
	start time.Time
}

func newCounter(max int, w time.Duration) *counter {
	return &counter{max: max, window: w, now: time.Now, hits: map[string]*slot{}}
}

// take records a request for key and returns 0 if allowed, or how long to wait.
func (l *counter) take(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.hits) > 10000 {
		for k, s := range l.hits {
			if now.Sub(s.start) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	s, ok := l.hits[key]
	if !ok || now.Sub(s.start) >= l.window {
		l.hits[key] = &slot{count: 1, start: now}
		return 0
	}
	if s.count >= l.max {
		return l.window - now.Sub(s.start)
	}
	s.count++
	return 0
}

// TrustedProxies parses TRUSTED_PROXIES (comma-separated IPs/CIDRs). Empty
// means trust nobody: c.ClientIP() is the TCP peer and X-Forwarded-For is
// ignored, so per-IP limits cannot be bypassed by a spoofed header.
func TrustedProxies() []string {
	var out []string
	for _, p := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
