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

func handlerTestToken(t *testing.T, userID int64, role, secret string) string {
	t.Helper()
	claims := &middleware.AuthClaims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return token
}

func newTestHandler(t *testing.T) (*Handler, *sqlx.DB) {
	t.Helper()
	db := handlerTestDB(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	logger := zap.NewNop()
	engine := billing.NewEngine(db, logger)
	t.Cleanup(func() { engine.Close(context.Background()) })
	h := NewHandler(db, engine, upstream.NewRouter("http://example.invalid", "mock", nil, logger), middleware.NewRateLimiter(rdb), "test-secret", logger, 100, "", 0, []string{"deepseek-chat"}, 1000, 100000)
	return h, db
}

func newTestRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/register", h.Register)
	r.Get("/api/models", h.ListModels)
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTAuth("test-secret"))
		r.Get("/api/me", h.GetMe)
		r.Get("/api/balance", h.GetBalance)
		r.Get("/api/keys", h.ListAPIKeys)
		r.Post("/api/keys", h.CreateAPIKey)
		r.Delete("/api/keys/{id}", h.DisableAPIKey)
		r.Get("/api/credits/ledger", h.ListMyLedger)
		r.Get("/api/usage", h.ListUsage)
	})
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTAuth("test-secret"))
		r.Use(middleware.RequireAdmin)
		r.Post("/api/admin/credits/grant", h.GrantCredits)
		r.Post("/api/admin/credits/revoke", h.RevokeCredits)
		r.Get("/api/admin/credits/operations", h.ListCreditOperations)
		r.Get("/api/admin/users", h.ListAdminUsers)
		r.Get("/api/admin/audit-logs", h.ListAuditLogs)
	})
	r.Post("/v1/chat/completions", h.Gateway)
	return r
}

func createTestUser(t *testing.T, db *sqlx.DB, role string, balance int64) int64 {
	t.Helper()
	username := fmt.Sprintf("u_%d", time.Now().UnixNano())
	result, err := db.Exec("INSERT INTO users (username, password_hash, role) VALUES (?, 'x', ?)", username, role)
	require.NoError(t, err)
	userID, err := result.LastInsertId()
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO balances (user_id, balance) VALUES (?, ?)", userID, balance)
	require.NoError(t, err)
	if balance > 0 {
		_, err = db.Exec(
			"INSERT INTO balance_logs (user_id, type, amount, balance_after, reference_type, reference_id, reason) VALUES (?, 'opening', ?, ?, 'test_opening', ?, 'test')",
			userID, balance, balance, userID)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID)
		db.Exec("DELETE FROM balance_holds WHERE user_id = ?", userID)
		db.Exec("DELETE FROM credit_operations WHERE user_id = ? OR operator_id = ?", userID, userID)
		db.Exec("DELETE FROM admin_audit_logs WHERE target_id = ? OR actor_id = ?", userID, userID)
		db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
		db.Exec("DELETE FROM users WHERE id = ?", userID)
	})
	return userID
}

func doJSON(t *testing.T, r http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestCreditGrantFlow(t *testing.T) {
	h, db := newTestHandler(t)
	r := newTestRouter(h)
	adminID := createTestUser(t, db, "admin", 0)
	userID := createTestUser(t, db, "user", 0)
	adminToken := handlerTestToken(t, adminID, "admin", "test-secret")
	userToken := handlerTestToken(t, userID, "user", "test-secret")

	grantBody := fmt.Sprintf(`{"user_id":%d,"credits":100,"reason":"内测额度"}`, userID)
	grant := httptest.NewRequest(http.MethodPost, "/api/admin/credits/grant", strings.NewReader(grantBody))
	grant.Header.Set("Authorization", "Bearer "+adminToken)
	grant.Header.Set("Content-Type", "application/json")
	grant.Header.Set("Idempotency-Key", "grant-1")
	grantRec := httptest.NewRecorder()
	r.ServeHTTP(grantRec, grant)
	require.Equal(t, http.StatusOK, grantRec.Code, grantRec.Body.String())

	var granted struct {
		BalanceTokens int64 `json:"balance_tokens"`
	}
	require.NoError(t, json.Unmarshal(grantRec.Body.Bytes(), &granted))
	assert.Equal(t, int64(100000), granted.BalanceTokens)

	repeat := httptest.NewRequest(http.MethodPost, "/api/admin/credits/grant", strings.NewReader(grantBody))
	repeat.Header.Set("Authorization", "Bearer "+adminToken)
	repeat.Header.Set("Content-Type", "application/json")
	repeat.Header.Set("Idempotency-Key", "grant-1")
	repeatRec := httptest.NewRecorder()
	r.ServeHTTP(repeatRec, repeat)
	require.Equal(t, http.StatusOK, repeatRec.Code, repeatRec.Body.String())

	var balance int64
	require.NoError(t, db.Get(&balance, "SELECT balance FROM balances WHERE user_id = ?", userID))
	assert.Equal(t, int64(100000), balance)

	revokeBody := fmt.Sprintf(`{"user_id":%d,"credits":40,"reason":"回收"}`, userID)
	revoke := httptest.NewRequest(http.MethodPost, "/api/admin/credits/revoke", strings.NewReader(revokeBody))
	revoke.Header.Set("Authorization", "Bearer "+adminToken)
	revoke.Header.Set("Content-Type", "application/json")
	revoke.Header.Set("Idempotency-Key", "revoke-1")
	revokeRec := httptest.NewRecorder()
	r.ServeHTTP(revokeRec, revoke)
	require.Equal(t, http.StatusOK, revokeRec.Code, revokeRec.Body.String())

	tooMuchBody := fmt.Sprintf(`{"user_id":%d,"credits":1000,"reason":"超额"}`, userID)
	tooMuch := httptest.NewRequest(http.MethodPost, "/api/admin/credits/revoke", strings.NewReader(tooMuchBody))
	tooMuch.Header.Set("Authorization", "Bearer "+adminToken)
	tooMuch.Header.Set("Content-Type", "application/json")
	tooMuchRec := httptest.NewRecorder()
	r.ServeHTTP(tooMuchRec, tooMuch)
	assert.Equal(t, http.StatusConflict, tooMuchRec.Code, tooMuchRec.Body.String())

	forbidden := httptest.NewRequest(http.MethodPost, "/api/admin/credits/grant", strings.NewReader(grantBody))
	forbidden.Header.Set("Authorization", "Bearer "+userToken)
	forbidden.Header.Set("Content-Type", "application/json")
	forbiddenRec := httptest.NewRecorder()
	r.ServeHTTP(forbiddenRec, forbidden)
	assert.Equal(t, http.StatusForbidden, forbiddenRec.Code, forbiddenRec.Body.String())

	me := doJSON(t, r, http.MethodGet, "/api/me", userToken, "")
	require.Equal(t, http.StatusOK, me.Code, me.Body.String())
	var meBody struct {
		Role    string `json:"role"`
		Tokens  int64  `json:"tokens"`
		Credits string `json:"credits"`
	}
	require.NoError(t, json.Unmarshal(me.Body.Bytes(), &meBody))
	assert.Equal(t, "user", meBody.Role)
	assert.Equal(t, int64(60000), meBody.Tokens)
	assert.Equal(t, "60.00", meBody.Credits)

	ledger := doJSON(t, r, http.MethodGet, "/api/credits/ledger", userToken, "")
	require.Equal(t, http.StatusOK, ledger.Code, ledger.Body.String())
	var ledgerRows []struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(ledger.Body.Bytes(), &ledgerRows))
	assert.GreaterOrEqual(t, len(ledgerRows), 2)

	audit := doJSON(t, r, http.MethodGet, "/api/admin/audit-logs", adminToken, "")
	require.Equal(t, http.StatusOK, audit.Code, audit.Body.String())
	var auditRows []struct {
		Action string `json:"action"`
	}
	require.NoError(t, json.Unmarshal(audit.Body.Bytes(), &auditRows))
	assert.GreaterOrEqual(t, len(auditRows), 2)
}

func TestAPIKeyAndChatFlow(t *testing.T) {
	h, db := newTestHandler(t)
	r := newTestRouter(h)
	userID := createTestUser(t, db, "user", 100000)
	token := handlerTestToken(t, userID, "user", "test-secret")

	createKey := doJSON(t, r, http.MethodPost, "/api/keys", token, `{"name":"gateway-test","expires_in_days":1}`)
	require.Equal(t, http.StatusCreated, createKey.Code, createKey.Body.String())
	var created struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	require.NoError(t, json.Unmarshal(createKey.Body.Bytes(), &created))
	require.NotEmpty(t, created.Key)

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

	var balance int64
	require.NoError(t, db.Get(&balance, "SELECT balance FROM balances WHERE user_id = ?", userID))
	assert.Less(t, balance, int64(100000))

	usage := doJSON(t, r, http.MethodGet, "/api/usage", token, "")
	require.Equal(t, http.StatusOK, usage.Code, usage.Body.String())
	var usageRows []struct {
		TotalTokens int64 `json:"total_tokens"`
	}
	require.NoError(t, json.Unmarshal(usage.Body.Bytes(), &usageRows))
	assert.Len(t, usageRows, 2)

	disable := doJSON(t, r, http.MethodDelete, fmt.Sprintf("/api/keys/%d", created.ID), token, "")
	require.Equal(t, http.StatusOK, disable.Code, disable.Body.String())

	disabledChat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}]}`))
	disabledChat.Header.Set("X-API-Key", created.Key)
	disabledChat.Header.Set("Content-Type", "application/json")
	disabledRec := httptest.NewRecorder()
	r.ServeHTTP(disabledRec, disabledChat)
	assert.Equal(t, http.StatusUnauthorized, disabledRec.Code, disabledRec.Body.String())
}

func TestRegisterInviteCode(t *testing.T) {
	h, db := newTestHandler(t)
	h.inviteCode = "invite-123"
	h.initialBalance = 1000
	r := newTestRouter(h)
	username := fmt.Sprintf("invite_%d", time.Now().UnixNano())

	noInvite := doJSON(t, r, http.MethodPost, "/api/register", "", fmt.Sprintf(`{"username":"%s","password":"testpass123"}`, username))
	assert.Equal(t, http.StatusForbidden, noInvite.Code)

	withInvite := doJSON(t, r, http.MethodPost, "/api/register", "", fmt.Sprintf(`{"username":"%s","password":"testpass123","invite_code":"invite-123"}`, username))
	require.Equal(t, http.StatusCreated, withInvite.Code, withInvite.Body.String())

	var userID int64
	require.NoError(t, db.Get(&userID, "SELECT id FROM users WHERE username = ?", username))
	t.Cleanup(func() {
		db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID)
		db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
		db.Exec("DELETE FROM users WHERE id = ?", userID)
	})
	var balance int64
	require.NoError(t, db.Get(&balance, "SELECT balance FROM balances WHERE user_id = ?", userID))
	assert.Equal(t, int64(1000), balance)
	var logs int
	require.NoError(t, db.Get(&logs, "SELECT COUNT(*) FROM balance_logs WHERE user_id = ? AND type = 'grant'", userID))
	assert.Equal(t, 1, logs)
}
