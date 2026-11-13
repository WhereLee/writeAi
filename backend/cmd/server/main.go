package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/lrs/llm-api-gateway/internal/billing"
	"github.com/lrs/llm-api-gateway/internal/config"
	"github.com/lrs/llm-api-gateway/internal/handler"
	"github.com/lrs/llm-api-gateway/internal/logger"
	"github.com/lrs/llm-api-gateway/internal/middleware"
	"github.com/lrs/llm-api-gateway/internal/store"
	"github.com/lrs/llm-api-gateway/internal/upstream"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}
	log, _ := logger.New(env)
	defer log.Sync()

	db, err := store.NewMySQL(cfg.MySQLDSN)
	if err != nil {
		log.Fatal("MySQL connect failed", zap.Error(err))
	}
	if err := store.Migrate(db); err != nil {
		log.Fatal("migrate failed", zap.Error(err))
	}
	log.Info("MySQL connected")
	for _, username := range cfg.BootstrapAdminUsernames {
		result, err := db.Exec("UPDATE users SET role = 'admin' WHERE username = ?", username)
		if err != nil {
			log.Error("bootstrap admin failed", zap.String("username", username), zap.Error(err))
			continue
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			log.Info("bootstrap admin promoted", zap.String("username", username))
		}
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatal("Redis connect failed", zap.Error(err))
	}
	log.Info("Redis connected")

	billingEngine := billing.NewEngine(db, log)
	modelRoutes := make(map[string]upstream.Route, len(cfg.ModelRoutes))
	for model, route := range cfg.ModelRoutes {
		modelRoutes[model] = upstream.Route{URL: route.URL, Key: route.Key}
	}
	upstreamRouter := upstream.NewRouter(cfg.UpstreamURL, cfg.UpstreamKey, modelRoutes, log)
	rateLimiter := middleware.NewRateLimiter(rdb)
	h := handler.NewHandler(db, billingEngine, upstreamRouter, rateLimiter, cfg.JWTSecret, log, cfg.RateLimitPerMin, cfg.RegistrationInviteCode, cfg.InitialBalanceTokens, cfg.ModelCatalog, cfg.TokensPerCredit, cfg.MaxGrantCreditsPerOp)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(middleware.CORS(cfg.CORSOrigins))
	r.Use(chimw.Timeout(10 * time.Minute))

	authLimiter := middleware.IPRateLimit(rdb, cfg.AuthRateLimitPerMin, time.Minute)
	adminLimiter := middleware.IPRateLimit(rdb, 30, time.Minute)
	r.With(authLimiter).Post("/api/register", h.Register)
	r.With(authLimiter).Post("/api/login", h.Login)
	r.Get("/api/models", h.ListModels)
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTAuth(cfg.JWTSecret))
		r.Get("/api/me", h.GetMe)
		r.Get("/api/keys", h.ListAPIKeys)
		r.Post("/api/keys", h.CreateAPIKey)
		r.Delete("/api/keys/{id}", h.DisableAPIKey)
		r.Get("/api/balance", h.GetBalance)
		r.Get("/api/credits/ledger", h.ListMyLedger)
		r.Get("/api/usage", h.ListUsage)
	})
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTAuth(cfg.JWTSecret))
		r.Use(middleware.RequireAdmin)
		r.Use(adminLimiter)
		r.Post("/api/admin/credits/grant", h.GrantCredits)
		r.Post("/api/admin/credits/revoke", h.RevokeCredits)
		r.Get("/api/admin/credits/operations", h.ListCreditOperations)
		r.Get("/api/admin/users", h.ListAdminUsers)
		r.Get("/api/admin/audit-logs", h.ListAuditLogs)
	})
	r.Post("/v1/chat/completions", h.Gateway)

	r.Handle("/metrics", promhttp.Handler())
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := rdb.Ping(r.Context()).Err(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("redis not ready"))
			return
		}
		if err := db.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("mysql not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})

	addr := ":" + cfg.Port
	if cfg.BindAddr != "" {
		addr = net.JoinHostPort(cfg.BindAddr, cfg.Port)
	}
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Info("server starting", zap.String("port", cfg.Port), zap.String("env", env))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("server error", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)

	billingEngine.Close(ctx)

	db.Close()
	rdb.Close()
	log.Info("shutdown complete")
}
