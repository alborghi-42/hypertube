package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.HandleMethodNotAllowed = true
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.POST("/api/auth/login", func(c *gin.Context) { c.Status(http.StatusOK) })
	ssr := func(c *gin.Context) { c.String(http.StatusOK, "ssr") }
	router.NoRoute(backendError(http.StatusNotFound, "Not found", ssr))
	router.NoMethod(backendError(http.StatusMethodNotAllowed, "Method not allowed", ssr))
	return router
}

func TestFallbackRouting(t *testing.T) {
	cases := []struct {
		method, path string
		status       int
		body, allow  string
	}{
		{http.MethodGet, "/api/nope", http.StatusNotFound, `{"error":"Not found"}`, ""},
		{http.MethodGet, "/api", http.StatusNotFound, `{"error":"Not found"}`, ""},
		{http.MethodGet, "/stream/x/y", http.StatusNotFound, `{"error":"Not found"}`, ""},
		{http.MethodGet, "/healthz/x", http.StatusNotFound, `{"error":"Not found"}`, ""},
		{http.MethodGet, "/api/auth/login", http.StatusMethodNotAllowed, `{"error":"Method not allowed"}`, "POST"},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed, `{"error":"Method not allowed"}`, "GET"},
		{http.MethodGet, "/movies/42", http.StatusOK, "ssr", ""},
		{http.MethodPost, "/login", http.StatusOK, "ssr", ""},
		{http.MethodGet, "/apix", http.StatusOK, "ssr", ""},
	}
	router := newTestRouter()
	for _, tc := range cases {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || w.Body.String() != tc.body || w.Header().Get("Allow") != tc.allow {
			t.Errorf("%s %s: got %d %q Allow=%q, want %d %q Allow=%q",
				tc.method, tc.path, w.Code, w.Body.String(), w.Header().Get("Allow"), tc.status, tc.body, tc.allow)
		}
	}
}
