package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthorize(t *testing.T) {
	s := &Server{token: "token", limiter: newAuthLimiter(10, time.Minute, 10)}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	if s.authorize(w, req) {
		t.Fatal("expected unauthorized without header")
	}
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Result().StatusCode)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer token")
	w = httptest.NewRecorder()
	if !s.authorize(w, req) {
		t.Fatal("expected authorized with correct token")
	}

	s = &Server{token: ""}
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w = httptest.NewRecorder()
	if !s.authorize(w, req) {
		t.Fatal("expected authorized when token not configured")
	}
}

func TestAuthorizeRateLimit(t *testing.T) {
	s := &Server{token: "token", limiter: newAuthLimiter(1, time.Minute, 10)}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	if s.authorize(w, req) {
		t.Fatal("expected unauthorized without header")
	}
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Result().StatusCode)
	}

	w = httptest.NewRecorder()
	if s.authorize(w, req) {
		t.Fatal("expected unauthorized without header")
	}
	if w.Result().StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Result().StatusCode)
	}
}

func TestAuthorizeAllowlist(t *testing.T) {
	allowlist, err := ParseCIDRAllowlist("192.0.2.0/24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := &Server{token: "", limiter: newAuthLimiter(10, time.Minute, 10), allow: allowlist}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "198.51.100.10:1234"
	w := httptest.NewRecorder()
	if s.authorize(w, req) {
		t.Fatal("expected denied for non-allowlisted host")
	}
	if w.Result().StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Result().StatusCode)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	w = httptest.NewRecorder()
	if !s.authorize(w, req) {
		t.Fatal("expected allowed for allowlisted host")
	}
}
