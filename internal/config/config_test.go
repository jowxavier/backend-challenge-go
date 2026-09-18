package config

import (
	"os"
	"testing"
	"time"
)

func TestDatabaseConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		valid     bool
	}{
		{"missing", "", false},
		{"provided", "postgres://localhost/example", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tc.url)
			t.Setenv("HTTP_PORT", "8080")
			cfg, err := Load()
			if !tc.valid {
				if err == nil {
					t.Fatal("missing URL accepted")
				}
				return
			}
			if err != nil || cfg.DatabaseURL != tc.url {
				t.Fatalf("configuration not preserved: %v", err)
			}
		})
	}
}

func TestReferencePendingTTL(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"24h", true}, {"1m", true}, {"", false}, {"0s", false}, {"-1h", false}, {"bad", false}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/example")
			t.Setenv("HTTP_PORT", "8080")
			t.Setenv("REFERENCE_PENDING_TTL", tc.value)
			cfg, err := Load()
			if (err == nil) != tc.valid {
				t.Fatal(cfg, err)
			}
			if tc.valid {
				want, _ := time.ParseDuration(tc.value)
				if cfg.ReferencePendingTTL != want {
					t.Fatal(cfg.ReferencePendingTTL)
				}
			}
		})
	}
	t.Run("default", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/example")
		t.Setenv("HTTP_PORT", "8080")
		t.Setenv("REFERENCE_PENDING_TTL", "temporary")
		if err := os.Unsetenv("REFERENCE_PENDING_TTL"); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load()
		if err != nil || cfg.ReferencePendingTTL != 24*time.Hour {
			t.Fatal(cfg, err)
		}
	})
}
