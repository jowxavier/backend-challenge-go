package config

import (
	"fmt"
	"os"
)

type Config struct {
	HTTP HTTPConfig
}

type HTTPConfig struct {
	Host string
	Port string
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTP: HTTPConfig{
			Host: getEnv("HTTP_HOST", "0.0.0.0"),
			Port: getEnv("HTTP_PORT", "8080"),
		},
	}

	if cfg.HTTP.Port == "" {
		return nil, fmt.Errorf("HTTP_PORT cannot be empty")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}

	return fallback
}
