package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	Messaging           MessagingConfig
	HTTP                HTTPConfig
	DatabaseURL         string
	ReferencePendingTTL time.Duration
}

type HTTPConfig struct {
	Host string
	Port string
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL: getEnv("DATABASE_URL", ""),
		HTTP: HTTPConfig{
			Host: getEnv("HTTP_HOST", "0.0.0.0"),
			Port: getEnv("HTTP_PORT", "8080"),
		},
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	if cfg.HTTP.Port == "" {
		return nil, fmt.Errorf("HTTP_PORT cannot be empty")
	}

	ttl, err := time.ParseDuration(getEnv("REFERENCE_PENDING_TTL", "24h"))
	if err != nil || ttl <= 0 {
		return nil, fmt.Errorf("REFERENCE_PENDING_TTL must be a positive duration")
	}
	cfg.ReferencePendingTTL = ttl
	cfg.Messaging, err = loadMessaging()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}

	return fallback
}
