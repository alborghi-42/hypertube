package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse 1")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword("correct horse 1", hash); err != nil || !ok {
		t.Fatalf("expected match, got ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong horse 1", hash); ok {
		t.Fatal("expected mismatch")
	}
	if _, err := VerifyPassword("x", "$2a$10$bcrypt"); err == nil {
		t.Fatal("expected format error")
	}
}

func TestRegisterValidation(t *testing.T) {
	valid := registerRequest{Email: " Jo@Example.com ", Username: "jo_42", FirstName: "Jo", LastName: "Doe", Password: "secret123"}
	valid.normalize()
	if errs := valid.validate(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if valid.Email != "jo@example.com" {
		t.Fatalf("email not normalized: %q", valid.Email)
	}
	bad := registerRequest{Email: "nope", Username: "a b", Password: "short"}
	errs := bad.validate()
	for _, f := range []string{"email", "username", "first_name", "last_name", "password"} {
		if errs[f] == "" {
			t.Errorf("expected error for %s", f)
		}
	}
}

func TestFailureLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := newFailureLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	l.fail("k")
	if l.blocked("k") != 0 {
		t.Fatal("blocked too early")
	}
	l.fail("k")
	if l.blocked("k") == 0 {
		t.Fatal("expected block")
	}
	now = now.Add(time.Minute)
	if l.blocked("k") != 0 {
		t.Fatal("window should have expired")
	}
}

func TestCSRF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRF(CookieOptions{Path: "/hypertube", Secure: true}))
	r.Any("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != CSRFCookie || cookies[0].Path != "/hypertube" || cookies[0].HttpOnly {
		t.Fatalf("unexpected cookies: %v", cookies)
	}
	token := cookies[0].Value

	post := func(header string) int {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.AddCookie(&http.Cookie{Name: CSRFCookie, Value: token})
		if header != "" {
			req.Header.Set(CSRFHeader, header)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := post(""); code != http.StatusForbidden {
		t.Fatalf("missing header: got %d", code)
	}
	if code := post("forged"); code != http.StatusForbidden {
		t.Fatalf("wrong header: got %d", code)
	}
	if code := post(token); code != http.StatusOK {
		t.Fatalf("valid header: got %d", code)
	}
}
