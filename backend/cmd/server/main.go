package main

import (
	"context"
	"fmt"
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

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatal("Redis connect failed", zap.Error(err))
	}
	log.Info("Redis connected")

	billingEngine := billing.NewEngine(rdb, db, log)
	upstreamClient := upstream.NewClient(cfg.UpstreamURL, cfg.UpstreamKey, log)
	rateLimiter := middleware.NewRateLimiter(rdb)
	h := handler.NewHandler(db, billingEngine, upstreamClient, rateLimiter, cfg.JWTSecret, log, cfg.RateLimitPerMin)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(35 * time.Second))

	r.Post("/api/register", h.Register)
	r.Post("/api/login", h.Login)
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTAuth(cfg.JWTSecret))
		r.Get("/api/keys", h.ListAPIKeys)
		r.Post("/api/keys", h.CreateAPIKey)
		r.Get("/api/balance", h.GetBalance)
		r.Get("/api/plans", h.ListPlans)
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

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 35 * time.Second,
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

	// Get all user IDs from Redis and sync balances
	userIDs := getUserIDsFromRedis(ctx, rdb)
	billingEngine.Close(ctx, userIDs)

	db.Close()
	rdb.Close()
	log.Info("shutdown complete")
}

func getUserIDsFromRedis(ctx context.Context, rdb *redis.Client) []int64 {
	keys, err := rdb.Keys(ctx, "balance:*").Result()
	if err != nil {
		return nil
	}
	ids := make([]int64, 0, len(keys))
	for _, key := range keys {
		var id int64
		fmt.Sscanf(key, "balance:%d", &id)
		if id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

