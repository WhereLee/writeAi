package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Port                    string
	BindAddr                string
	MySQLDSN                string
	RedisAddr               string
	JWTSecret               string
	UpstreamURL             string
	UpstreamKey             string
	RateLimitPerMin         int
	CORSOrigins             []string
	RegistrationInviteCode  string
	InitialBalanceTokens    int64
	AuthRateLimitPerMin     int
	ModelCatalog            []string
	ModelRoutes             map[string]ModelRoute
	TokensPerCredit         int64
	MaxGrantCreditsPerOp    int64
	BootstrapAdminUsernames []string
}

type ModelRoute struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:                    getEnv("PORT", "8080"),
		BindAddr:                getEnv("BIND_ADDR", ""),
		MySQLDSN:                getEnv("MYSQL_DSN", ""),
		RedisAddr:               getEnv("REDIS_ADDR", "127.0.0.1:6379"),
		JWTSecret:               getEnv("JWT_SECRET", ""),
		UpstreamURL:             getEnv("UPSTREAM_URL", ""),
		UpstreamKey:             getEnv("UPSTREAM_KEY", ""),
		RateLimitPerMin:         100,
		CORSOrigins:             splitList(getEnv("CORS_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000,http://localhost:5173,http://127.0.0.1:5173,http://118.31.111.223:3000,http://118.31.111.223:8080,https://118.31.111.223:8080,https://118-31-111-223.sslip.io")),
		RegistrationInviteCode:  getEnv("REGISTRATION_INVITE_CODE", ""),
		InitialBalanceTokens:    100000,
		AuthRateLimitPerMin:     20,
		ModelCatalog:            splitList(getEnv("MODEL_CATALOG", "deepseek-chat")),
		ModelRoutes:             map[string]ModelRoute{},
		TokensPerCredit:         1000,
		MaxGrantCreditsPerOp:    100000,
		BootstrapAdminUsernames: splitList(getEnv("BOOTSTRAP_ADMIN_USERNAMES", "")),
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
	if v := os.Getenv("INITIAL_BALANCE_TOKENS"); v != "" {
		n := int64(0)
		fmt.Sscanf(v, "%d", &n)
		if n >= 0 {
			cfg.InitialBalanceTokens = n
		}
	}
	if v := os.Getenv("AUTH_RATE_LIMIT_PER_MIN"); v != "" {
		n := 0
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			cfg.AuthRateLimitPerMin = n
		}
	}
	if v := os.Getenv("TOKENS_PER_CREDIT"); v != "" {
		n := int64(0)
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			cfg.TokensPerCredit = n
		}
	}
	if v := os.Getenv("MAX_GRANT_CREDITS_PER_OP"); v != "" {
		n := int64(0)
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			cfg.MaxGrantCreditsPerOp = n
		}
	}
	if v := os.Getenv("MODEL_ROUTES"); v != "" {
		if err := json.Unmarshal([]byte(v), &cfg.ModelRoutes); err != nil {
			return nil, fmt.Errorf("parse MODEL_ROUTES: %w", err)
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

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
