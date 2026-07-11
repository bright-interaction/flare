package config

import (
	"strings"
	"testing"
)

// prodEnv sets the minimum env for a production Load, letting a test omit one
// key to assert it is required.
func prodEnv(t *testing.T, omit string) {
	t.Helper()
	vals := map[string]string{
		"ENVIRONMENT":      "production",
		"DATABASE_URL":     "postgres://localhost/flare",
		"SESSION_KEY":      "0123456789abcdef0123456789abcdef0123456789abcdef",
		"CSRF_KEY":         "0123456789abcdef0123456789abcdef",
		"FLARE_SECRET_KEY": "0123456789abcdef0123456789abcdef",
	}
	for k, v := range vals {
		if k == omit {
			continue
		}
		t.Setenv(k, v)
	}
	if omit != "" {
		t.Setenv(omit, "")
	}
}

// TestProductionRequiresFlareSecretKey locks the fail-closed guard: without
// FLARE_SECRET_KEY a production instance must refuse to start, because
// integration secrets (BYOAI key, OIDC secret, GitHub token) would otherwise be
// stored plaintext.
func TestProductionRequiresFlareSecretKey(t *testing.T) {
	prodEnv(t, "FLARE_SECRET_KEY")
	_, err := Load()
	if err == nil {
		t.Fatal("production Load without FLARE_SECRET_KEY must error")
	}
	if !strings.Contains(err.Error(), "FLARE_SECRET_KEY") {
		t.Errorf("error should name FLARE_SECRET_KEY, got: %v", err)
	}
}

// TestProductionRequiresSessionAndCSRF confirms the other required secrets still
// fail closed (guards against a refactor dropping one).
func TestProductionRequiresSessionAndCSRF(t *testing.T) {
	for _, key := range []string{"SESSION_KEY", "CSRF_KEY"} {
		prodEnv(t, key)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("production Load without %s must error naming it, got: %v", key, err)
		}
	}
}

// TestProductionLoadsWithAllSecrets confirms a fully-configured production env
// loads cleanly (so the guard is not over-broad).
func TestProductionLoadsWithAllSecrets(t *testing.T) {
	prodEnv(t, "")
	if _, err := Load(); err != nil {
		t.Fatalf("production Load with all secrets set should succeed, got: %v", err)
	}
}
