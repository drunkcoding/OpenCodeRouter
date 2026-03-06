package auth

import (
	"os"
	"reflect"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Enabled {
		t.Fatal("expected auth disabled by default")
	}
	if len(cfg.CORSAllowedOrigins) != 1 || cfg.CORSAllowedOrigins[0] != "*" {
		t.Fatalf("unexpected default CORS origins: %#v", cfg.CORSAllowedOrigins)
	}
	if _, ok := cfg.BypassPaths["/api/health"]; !ok {
		t.Fatal("expected /api/health bypass by default")
	}
	if _, ok := cfg.BypassPaths["/api/backends"]; !ok {
		t.Fatal("expected /api/backends bypass by default")
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv(envAuthEnabled, "true")
	t.Setenv(envBearerTokens, "tok-a, tok-b")
	t.Setenv(envBasicAuth, "alice:secret,bob:pw")
	t.Setenv(envCORSAllowOrigins, "https://a.example,https://b.example")

	cfg := LoadFromEnv()
	if !cfg.Enabled {
		t.Fatal("expected enabled from env")
	}
	if !reflect.DeepEqual(cfg.BearerTokens, []string{"tok-a", "tok-b"}) {
		t.Fatalf("unexpected tokens: %#v", cfg.BearerTokens)
	}
	if got := cfg.BasicAuth["alice"]; got != "secret" {
		t.Fatalf("unexpected alice password: %q", got)
	}
	if got := cfg.BasicAuth["bob"]; got != "pw" {
		t.Fatalf("unexpected bob password: %q", got)
	}
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, []string{"https://a.example", "https://b.example"}) {
		t.Fatalf("unexpected CORS origins: %#v", cfg.CORSAllowedOrigins)
	}
}

func TestLoadFromEnv_InvalidBasicEntriesIgnored(t *testing.T) {
	t.Setenv(envBasicAuth, "bad,no-colon,ok:yes,:missing-user,user-only:")
	cfg := LoadFromEnv()
	if len(cfg.BasicAuth) != 1 {
		t.Fatalf("expected 1 valid basic entry, got %d", len(cfg.BasicAuth))
	}
	if cfg.BasicAuth["ok"] != "yes" {
		t.Fatal("expected ok:yes to be parsed")
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" a, ,b ,, c ")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCSV mismatch: got %#v want %#v", got, want)
	}
}

func TestLoadFromEnv_RespectsUnset(t *testing.T) {
	_ = os.Unsetenv(envAuthEnabled)
	_ = os.Unsetenv(envBearerTokens)
	_ = os.Unsetenv(envBasicAuth)
	_ = os.Unsetenv(envCORSAllowOrigins)

	cfg := LoadFromEnv()
	if cfg.Enabled {
		t.Fatal("expected disabled when unset")
	}
}
