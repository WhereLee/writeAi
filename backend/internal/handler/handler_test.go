package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	db.Exec("DELETE FROM api_keys WHERE user_id = ?", userID)
	_, err := db.Exec("INSERT INTO balances (user_id, balance) VALUES (?, 0)", userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID)
		db.Exec("DELETE FROM orders WHERE user_id = ?", userID)
		db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
		db.Exec("DELETE FROM api_keys WHERE user_id = ?", userID)
	})

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	logger := zap.NewNop()
	engine := billing.NewEngine(rdb, db, logger)
	t.Cleanup(engine.ShutdownCompensation)
	h := NewHandler(db, engine, upstream.NewRouter("http://example.invalid", "mock", nil, logger), middleware.NewRateLimiter(rdb), "test-secret", logger, 100, true, "", 100000, []string{"deepseek-chat"})

	r := chi.NewRouter()
	r.Get("/api/plans", h.ListPlans)
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTAuth("test-secret"))
		r.Get("/api/orders", h.ListOrders)
		r.Post("/api/orders", h.CreateOrder)
		r.Post("/api/orders/{id}/pay", h.PayOrder)
		r.Get("/api/keys", h.ListAPIKeys)
		r.Post("/api/keys", h.CreateAPIKey)
		r.Delete("/api/keys/{id}", h.DisableAPIKey)
	})
	r.Post("/v1/chat/completions", h.Gateway)
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

	fullKey := "sk-test-disable"
	sum := sha256.Sum256([]byte(fullKey))
	keyResult, err := db.Exec("INSERT INTO api_keys (user_id, key_hash, key_prefix, name) VALUES (?, ?, ?, ?)", userID, hex.EncodeToString(sum[:]), fullKey[:12], "test")
	require.NoError(t, err)
	keyID, err := keyResult.LastInsertId()
	require.NoError(t, err)
	delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/keys/%d", keyID), nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delRec := httptest.NewRecorder()
	r.ServeHTTP(delRec, delReq)
	require.Equal(t, http.StatusOK, delRec.Code, delRec.Body.String())
	var active bool
	require.NoError(t, db.Get(&active, "SELECT is_active FROM api_keys WHERE id = ?", keyID))
	assert.False(t, active)

	createKey := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"name":"gateway-test","expires_in_days":1}`))
	createKey.Header.Set("Authorization", "Bearer "+token)
	createKey.Header.Set("Content-Type", "application/json")
	createKeyRec := httptest.NewRecorder()
	r.ServeHTTP(createKeyRec, createKey)
	require.Equal(t, http.StatusCreated, createKeyRec.Code, createKeyRec.Body.String())
	var created struct {
		ID        int64  `json:"id"`
		Key       string `json:"key"`
		KeyPrefix string `json:"key_prefix"`
	}
	require.NoError(t, json.Unmarshal(createKeyRec.Body.Bytes(), &created))
	require.NotEmpty(t, created.Key)
	require.NotEmpty(t, created.KeyPrefix)

	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}]}`))
	chat.Header.Set("X-API-Key", created.Key)
	chat.Header.Set("Content-Type", "application/json")
	chatRec := httptest.NewRecorder()
	r.ServeHTTP(chatRec, chat)
	require.Equal(t, http.StatusOK, chatRec.Code, chatRec.Body.String())

	streamReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-chat","messages":[{"role":"user","content":"stream hello"}],"stream":true}`))
	streamReq.Header.Set("X-API-Key", created.Key)
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	r.ServeHTTP(streamRec, streamReq)
	require.Equal(t, http.StatusOK, streamRec.Code, streamRec.Body.String())
	assert.Contains(t, streamRec.Body.String(), "data:")
	assert.Contains(t, streamRec.Body.String(), "[DONE]")
}

func TestRegisterInviteCode(t *testing.T) {
	db := handlerTestDB(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	logger := zap.NewNop()
	engine := billing.NewEngine(rdb, db, logger)
	t.Cleanup(engine.ShutdownCompensation)
	h := NewHandler(db, engine, upstream.NewRouter("http://example.invalid", "mock", nil, logger), middleware.NewRateLimiter(rdb), "test-secret", logger, 100, false, "invite-123", 1000, []string{"deepseek-chat"})

	r := chi.NewRouter()
	r.Post("/api/register", h.Register)
	username := fmt.Sprintf("invite_%d", time.Now().UnixNano())

	noInvite := httptest.NewRequest(http.MethodPost, "/api/register", strings.NewReader(fmt.Sprintf(`{"username":"%s","password":"testpass123"}`, username)))
	noInvite.Header.Set("Content-Type", "application/json")
	noInviteRec := httptest.NewRecorder()
	r.ServeHTTP(noInviteRec, noInvite)
	assert.Equal(t, http.StatusForbidden, noInviteRec.Code)

	withInvite := httptest.NewRequest(http.MethodPost, "/api/register", strings.NewReader(fmt.Sprintf(`{"username":"%s","password":"testpass123","invite_code":"invite-123"}`, username)))
	withInvite.Header.Set("Content-Type", "application/json")
	withInviteRec := httptest.NewRecorder()
	r.ServeHTTP(withInviteRec, withInvite)
	require.Equal(t, http.StatusCreated, withInviteRec.Code, withInviteRec.Body.String())

	var userID int64
	require.NoError(t, db.Get(&userID, "SELECT id FROM users WHERE username = ?", username))
	t.Cleanup(func() {
		db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
		db.Exec("DELETE FROM users WHERE id = ?", userID)
	})
	var balance int64
	require.NoError(t, db.Get(&balance, "SELECT balance FROM balances WHERE user_id = ?", userID))
	assert.Equal(t, int64(1000), balance)
}
