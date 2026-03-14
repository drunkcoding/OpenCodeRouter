package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type RateLimiter interface {
	Allow(r *http.Request) bool
}

type NoopRateLimiter struct{}

func (NoopRateLimiter) Allow(_ *http.Request) bool { return true }

func Middleware(next http.Handler, cfg Config) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}

	if cfg.BasicAuth == nil {
		cfg.BasicAuth = map[string]string{}
	}

	return withRequestID(withCORS(withAuth(withRateLimit(next, NoopRateLimiter{}), cfg), cfg))
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if reqID == "" {
			reqID = newRequestID()
		}
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r)
	})
}

func withRateLimit(next http.Handler, limiter RateLimiter) http.Handler {
	if limiter == nil {
		limiter = NoopRateLimiter{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(r) {
			writeJSONError(w, http.StatusTooManyRequests, "rate_limited", "RATE_LIMITED", "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withCORS(next http.Handler, cfg Config) http.Handler {
	allowed := cfg.CORSAllowedOrigins
	if len(allowed) == 0 {
		allowed = []string{"*"}
	}

	allowAll := false
	allowSet := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		if origin == "*" {
			allowAll = true
			continue
		}
		allowSet[origin] = struct{}{}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			if allowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else if _, ok := allowSet[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func withAuth(next http.Handler, cfg Config) http.Handler {
	bypass := cfg.BypassPaths
	if bypass == nil {
		bypass = map[string]struct{}{}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bypass[r.URL.Path]; ok {
			next.ServeHTTP(w, r)
			return
		}

		if !cfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		authz := strings.TrimSpace(r.Header.Get("Authorization"))
		if validateBearer(authz, cfg.BearerTokens) || validateBasic(authz, cfg.BasicAuth) {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("WWW-Authenticate", `Bearer realm="opencoderouter", Basic realm="opencoderouter"`)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "UNAUTHORIZED", "invalid or missing credentials")
	})
}

func validateBearer(authz string, tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return false
	}
	token := strings.TrimSpace(authz[len("Bearer "):])
	if token == "" {
		return false
	}
	for _, allowed := range tokens {
		if subtle.ConstantTimeCompare([]byte(token), []byte(allowed)) == 1 {
			return true
		}
	}
	return false
}

func validateBasic(authz string, users map[string]string) bool {
	if len(users) == 0 {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(authz), "basic ") {
		return false
	}
	payload := strings.TrimSpace(authz[len("Basic "):])
	if payload == "" {
		return false
	}

	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return false
	}

	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return false
	}

	user := parts[0]
	pass := parts[1]
	stored, ok := users[user]
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(pass), []byte(stored)) == 1
}

func writeJSONError(w http.ResponseWriter, status int, errCode, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   errCode,
		"code":    code,
		"message": msg,
	})
}

func newRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000")))
	}
	return hex.EncodeToString(b)
}
