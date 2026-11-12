package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/lrs/llm-api-gateway/internal/billing"
	"github.com/lrs/llm-api-gateway/internal/middleware"
	"github.com/lrs/llm-api-gateway/internal/store"
	"github.com/lrs/llm-api-gateway/internal/upstream"
)

func handlerTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:root@tcp(127.0.0.1:3306)/llm_gateway?parseTime=true&charset=utf8mb4"
	}
	db, err := sqlx.Connect("mysql", dsn)
	if err != nil {
		if os.Getenv("REQUIRE_TEST_DB") == "true" {
			t.Fatalf("connect test database: %v", err)
		}
		t.Skipf("connect test database: %v", err)
	}
	require.NoError(t, store.Migrate(db))
	t.Cleanup(func() { db.Close() })
	return db
}

func handlerTestToken(t *testing.T, userID int64, secret string) string {
	t.Helper()
	claims := &middleware.AuthClaims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return token
}

func TestOrderPayFlow(t *testing.T) {
	db := handlerTestDB(t)
	const userID int64 = 910001
	db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID)
	db.Exec("DELETE FROM orders WHERE user_id = ?", userID)
	db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
	_, err := db.Exec("INSERT INTO balances (user_id, balance) VALUES (?, 0)", userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID)
		db.Exec("DELETE FROM orders WHERE user_id = ?", userID)
		db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
	})

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	logger := zap.NewNop()
	engine := billing.NewEngine(rdb, db, logger)
	t.Cleanup(engine.ShutdownCompensation)
	h := NewHandler(db, engine, upstream.NewClient("http://example.invalid", "mock", logger), middleware.NewRateLimiter(rdb), "test-secret", logger, 100, true)

	r := chi.NewRouter()
	r.Get("/api/plans", h.ListPlans)
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTAuth("test-secret"))
		r.Get("/api/orders", h.ListOrders)
		r.Post("/api/orders", h.CreateOrder)
		r.Post("/api/orders/{id}/pay", h.PayOrder)
	})
	token := handlerTestToken(t, userID, "test-secret")

	create := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(`{"plan_id":1}`))
	create.Header.Set("Authorization", "Bearer "+token)
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	r.ServeHTTP(createRec, create)
	require.Equal(t, http.StatusCreated, createRec.Code, createRec.Body.String())

	var order struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &order))
	require.NotZero(t, order.ID)

	pay := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/orders/%d/pay", order.ID), nil)
	pay.Header.Set("Authorization", "Bearer "+token)
	payRec := httptest.NewRecorder()
	r.ServeHTTP(payRec, pay)
	require.Equal(t, http.StatusOK, payRec.Code, payRec.Body.String())

	balance, err := engine.CheckBalance(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, int64(100000), balance)

	payAgain := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/orders/%d/pay", order.ID), nil)
	payAgain.Header.Set("Authorization", "Bearer "+token)
	payAgainRec := httptest.NewRecorder()
	r.ServeHTTP(payAgainRec, payAgain)
	assert.Equal(t, http.StatusConflict, payAgainRec.Code, payAgainRec.Body.String())

	var logs int
	require.NoError(t, db.Get(&logs, "SELECT COUNT(*) FROM balance_logs WHERE user_id = ? AND type = 'recharge'", userID))
	assert.Equal(t, 1, logs)
}
