package config

import "testing"

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
