package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddleware_ValidBearerTokenPasses(t *testing.T) {
	cfg := Defaults()
	cfg.Enabled = true
	cfg.BearerTokens = []string{"good-token"}

	called := false
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/resolve?name=test", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestMiddleware_InvalidTokenReturns401JSON(t *testing.T) {
	cfg := Defaults()
	cfg.Enabled = true
	cfg.BearerTokens = []string{"good-token"}

	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/resolve?name=test", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected JSON content type, got %q", ct)
	}

	var payload map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body, got error: %v", err)
	}
	if payload["error"] != "unauthorized" || payload["code"] != "UNAUTHORIZED" {
		t.Fatalf("unexpected error payload: %#v", payload)
	}
}

func TestMiddleware_ValidBasicAuthPasses(t *testing.T) {
	cfg := Defaults()
	cfg.Enabled = true
	cfg.BasicAuth = map[string]string{"alice": "secret"}

	called := false
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), cfg)

	basic := base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	req := httptest.NewRequest(http.MethodGet, "/api/resolve?name=test", nil)
	req.Header.Set("Authorization", "Basic "+basic)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestMiddleware_BypassHealthEndpoints(t *testing.T) {
	cfg := Defaults()
	cfg.Enabled = true
	cfg.BearerTokens = []string{"good-token"}

	called := false
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if !called {
		t.Fatal("expected bypass to call next handler")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for bypass endpoint, got %d", w.Code)
	}
}

func TestMiddleware_CORSAllowlist(t *testing.T) {
	cfg := Defaults()
	cfg.CORSAllowedOrigins = []string{"https://allowed.example"}

	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/resolve?name=test", nil)
	req.Header.Set("Origin", "https://allowed.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://allowed.example" {
		t.Fatalf("expected allowed origin echoed, got %q", got)
	}

	reqBlocked := httptest.NewRequest(http.MethodGet, "/api/resolve?name=test", nil)
	reqBlocked.Header.Set("Origin", "https://blocked.example")
	wBlocked := httptest.NewRecorder()
	h.ServeHTTP(wBlocked, reqBlocked)
	if got := wBlocked.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected blocked origin to be omitted, got %q", got)
	}
}

func TestMiddleware_SetsRequestIDHeader(t *testing.T) {
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), Defaults())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("expected X-Request-ID response header")
	}
}
