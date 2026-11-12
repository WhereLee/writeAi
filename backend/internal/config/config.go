package config

import (
	"fmt"
	"os"
)

type Config struct {
	Port            string
	MySQLDSN        string
	RedisAddr       string
	JWTSecret       string
	UpstreamURL     string
	UpstreamKey     string
	RateLimitPerMin int
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:            getEnv("PORT", "8080"),
		MySQLDSN:        getEnv("MYSQL_DSN", ""),
		RedisAddr:       getEnv("REDIS_ADDR", "127.0.0.1:6379"),
		JWTSecret:       getEnv("JWT_SECRET", ""),
		UpstreamURL:     getEnv("UPSTREAM_URL", ""),
		UpstreamKey:     getEnv("UPSTREAM_KEY", ""),
		RateLimitPerMin: 100,
	}

	if cfg.MySQLDSN == "" {
		return nil, fmt.Errorf("MYSQL_DSN is required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	if cfg.UpstreamURL == "" {
		return nil, fmt.Errorf("UPSTREAM_URL is required")
	}
	if cfg.UpstreamKey == "" {
		return nil, fmt.Errorf("UPSTREAM_KEY is required")
	}
	if v := os.Getenv("RATE_LIMIT_PER_MIN"); v != "" {
		n := 0
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			cfg.RateLimitPerMin = n
		}
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
