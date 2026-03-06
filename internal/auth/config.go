package auth

import (
	"os"
	"strings"
)

const (
	envAuthEnabled      = "OCR_AUTH_ENABLED"
	envBearerTokens     = "OCR_AUTH_BEARER_TOKENS"
	envBasicAuth        = "OCR_AUTH_BASIC"
	envCORSAllowOrigins = "OCR_CORS_ALLOW_ORIGINS"
)

type Config struct {
	Enabled            bool
	BearerTokens       []string
	BasicAuth          map[string]string
	CORSAllowedOrigins []string
	BypassPaths        map[string]struct{}
}

func Defaults() Config {
	return Config{
		Enabled:            false,
		BearerTokens:       nil,
		BasicAuth:          map[string]string{},
		CORSAllowedOrigins: []string{"*"},
		BypassPaths: map[string]struct{}{
			"/api/health":   {},
			"/api/backends": {},
		},
	}
}

func LoadFromEnv() Config {
	cfg := Defaults()

	if raw := strings.TrimSpace(os.Getenv(envAuthEnabled)); raw != "" {
		raw = strings.ToLower(raw)
		cfg.Enabled = raw == "1" || raw == "true" || raw == "yes" || raw == "on"
	}

	if raw := strings.TrimSpace(os.Getenv(envBearerTokens)); raw != "" {
		cfg.BearerTokens = splitCSV(raw)
	}

	if raw := strings.TrimSpace(os.Getenv(envBasicAuth)); raw != "" {
		pairs := splitCSV(raw)
		for _, pair := range pairs {
			parts := strings.SplitN(pair, ":", 2)
			if len(parts) != 2 {
				continue
			}
			user := strings.TrimSpace(parts[0])
			pass := strings.TrimSpace(parts[1])
			if user == "" || pass == "" {
				continue
			}
			cfg.BasicAuth[user] = pass
		}
	}

	if raw := strings.TrimSpace(os.Getenv(envCORSAllowOrigins)); raw != "" {
		origins := splitCSV(raw)
		if len(origins) > 0 {
			cfg.CORSAllowedOrigins = origins
		}
	}

	return cfg
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
