package handler

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"

	"github.com/lrs/llm-api-gateway/internal/billing"
	"github.com/lrs/llm-api-gateway/internal/metrics"
	"github.com/lrs/llm-api-gateway/internal/middleware"
	"github.com/lrs/llm-api-gateway/internal/model"
	"github.com/lrs/llm-api-gateway/internal/upstream"
)

type Handler struct {
	db              *sqlx.DB
	billing         *billing.Engine
	upstream        *upstream.Router
	rateLimiter     *middleware.RateLimiter
	jwtSecret       string
	logger          *zap.Logger
	rateLimitPerMin int
	inviteCode      string
	initialBalance  int64
	modelCatalog    []string
	tokensPerCredit int64
	maxGrantCredits int64
}

func NewHandler(db *sqlx.DB, be *billing.Engine, uc *upstream.Router, rl *middleware.RateLimiter, jwtSecret string, logger *zap.Logger, rateLimitPerMin int, inviteCode string, initialBalance int64, modelCatalog []string, tokensPerCredit, maxGrantCredits int64) *Handler {
	return &Handler{db: db, billing: be, upstream: uc, rateLimiter: rl, jwtSecret: jwtSecret, logger: logger, rateLimitPerMin: rateLimitPerMin, inviteCode: inviteCode, initialBalance: initialBalance, modelCatalog: modelCatalog, tokensPerCredit: tokensPerCredit, maxGrantCredits: maxGrantCredits}
}

type registerReq struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	InviteCode string `json:"invite_code"`
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.Username) < 3 || len(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username must be 3+ chars, password 6+ chars"})
		return
	}
	if h.inviteCode != "" && subtle.ConstantTimeCompare([]byte(req.InviteCode), []byte(h.inviteCode)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid invite code"})
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		h.logger.Error("hash password failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	tx, err := h.db.BeginTxx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin register transaction failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(r.Context(), "INSERT INTO users (username, password_hash) VALUES (?, ?)", req.Username, hash)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "username already exists"})
		return
	}
	userID, _ := result.LastInsertId()
	if _, err := tx.ExecContext(r.Context(), "INSERT INTO balances (user_id, balance) VALUES (?, ?)", userID, h.initialBalance); err != nil {
		h.logger.Error("create balance failed", zap.Int64("user_id", userID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if h.initialBalance > 0 {
		if _, err := tx.ExecContext(r.Context(),
			"INSERT INTO balance_logs (user_id, type, amount, balance_after, reference_type, reference_id, reason) VALUES (?, 'grant', ?, ?, 'registration', ?, '注册赠送')",
			userID, h.initialBalance, h.initialBalance, userID); err != nil {
			h.logger.Error("write opening balance log failed", zap.Int64("user_id", userID), zap.Error(err))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		h.logger.Error("commit register failed", zap.Int64("user_id", userID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	h.logger.Info("user registered", zap.Int64("user_id", userID), zap.String("username", req.Username))
	writeJSON(w, http.StatusCreated, map[string]interface{}{"user_id": userID, "username": req.Username})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	var user model.User
	err := h.db.Get(&user, "SELECT id, username, password_hash, role FROM users WHERE username = ?", req.Username)
	if err != nil {
		h.logger.Warn("login failed", zap.String("username", req.Username), zap.Error(err))
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	if !checkPassword(req.Password, user.PasswordHash) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	token, err := generateJWT(user.ID, user.Role, h.jwtSecret)
	if err != nil {
		h.logger.Error("generate jwt failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"token": token, "user_id": user.ID, "role": user.Role})
}

type createKeyReq struct {
	Name          string `json:"name"`
	ExpiresInDays int    `json:"expires_in_days"`
}

func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req createKeyReq
	json.NewDecoder(r.Body).Decode(&req)
	key := "sk-" + newUUID()
	keyHash := hashAPIKey(key)
	keyPrefix := key[:12]
	var result sql.Result
	var err error
	if req.ExpiresInDays > 0 {
		result, err = h.db.Exec("INSERT INTO api_keys (user_id, key_hash, key_prefix, name, expires_at) VALUES (?, ?, ?, ?, DATE_ADD(NOW(), INTERVAL ? DAY))", userID, keyHash, keyPrefix, req.Name, req.ExpiresInDays)
	} else {
		result, err = h.db.Exec("INSERT INTO api_keys (user_id, key_hash, key_prefix, name) VALUES (?, ?, ?, ?)", userID, keyHash, keyPrefix, req.Name)
	}
	if err != nil {
		h.logger.Error("create api key failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	keyID, _ := result.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": keyID, "key": key, "key_prefix": keyPrefix, "name": req.Name})
}

func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var keys []model.APIKey
	err := h.db.Select(&keys, "SELECT id, user_id, key_prefix, name, is_active, expires_at, last_used_at, created_at FROM api_keys WHERE user_id = ?", userID)
	if err != nil {
		h.logger.Error("list api keys failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func hashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) DisableAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	keyID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid key id"})
		return
	}

	result, err := h.db.ExecContext(r.Context(), "UPDATE api_keys SET is_active = FALSE WHERE id = ? AND user_id = ?", keyID, userID)
	if err != nil {
		h.logger.Error("disable api key failed", zap.Int64("user_id", userID), zap.Int64("key_id", keyID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": keyID, "is_active": false})
}

func (h *Handler) GetBalance(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	bal, err := h.billing.CheckBalance(r.Context(), userID)
	if err != nil {
		h.logger.Error("get balance failed", zap.Int64("user_id", userID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user_id":           userID,
		"balance":           bal,
		"tokens":            bal,
		"credits":           h.formatCredits(bal),
		"tokens_per_credit": h.tokensPerCredit,
	})
}

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var user model.User
	if err := h.db.GetContext(r.Context(), &user, "SELECT id, username, role, created_at FROM users WHERE id = ?", userID); err != nil {
		h.logger.Error("get user failed", zap.Int64("user_id", userID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	bal, err := h.billing.CheckBalance(r.Context(), userID)
	if err != nil {
		h.logger.Error("get balance failed", zap.Int64("user_id", userID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user_id":           user.ID,
		"username":          user.Username,
		"role":              user.Role,
		"tokens":            bal,
		"credits":           h.formatCredits(bal),
		"tokens_per_credit": h.tokensPerCredit,
	})
}

func (h *Handler) formatCredits(tokens int64) string {
	if h.tokensPerCredit <= 0 {
		return "0.00"
	}
	return fmt.Sprintf("%.2f", float64(tokens)/float64(h.tokensPerCredit))
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.modelCatalog)
}

type gatewayRequest struct {
	Model     string             `json:"model"`
	Messages  []upstream.Message `json:"messages"`
	Stream    bool               `json:"stream"`
	MaxTokens int64              `json:"max_tokens,omitempty"`
}

func (h *Handler) Gateway(w http.ResponseWriter, r *http.Request) {
	metrics.ActiveRequests.Inc()
	defer metrics.ActiveRequests.Dec()

	start := time.Now()
	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing API key"})
		return
	}

	var ak model.APIKey
	keyHash := hashAPIKey(apiKey)
	err := h.db.Get(&ak, "SELECT id, user_id, key_prefix, is_active, expires_at FROM api_keys WHERE key_hash = ? AND is_active = TRUE AND (expires_at IS NULL OR expires_at > NOW())", keyHash)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or inactive API key"})
		return
	}
	h.db.Exec("UPDATE api_keys SET last_used_at = NOW() WHERE id = ?", ak.ID)

	allowed, err := h.rateLimiter.Limit(r.Context(), ak.UserID, h.rateLimitPerMin, time.Minute)
	if err != nil {
		h.logger.Error("rate limiter unavailable", zap.Int64("user_id", ak.UserID), zap.Error(err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "rate limiter unavailable"})
		return
	}
	if !allowed {
		metrics.RateLimited.Inc()
		h.logger.Warn("rate limited", zap.Int64("user_id", ak.UserID))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
		return
	}

	var req gatewayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
		return
	}
	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messages is required"})
		return
	}

	estimatedTokens := int64(100)
	for _, m := range req.Messages {
		estimatedTokens += int64(len(m.Content)) / 4
	}
	if req.MaxTokens > 0 {
		estimatedTokens += req.MaxTokens
	} else {
		estimatedTokens += 1024
	}
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = newUUID()
	}

	if err := h.billing.ReserveWithIdempotency(r.Context(), ak.UserID, estimatedTokens, requestID); err != nil {
		if err == billing.ErrDuplicateRequest {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "duplicate request"})
			return
		}
		if err == billing.ErrInsufficientBalance {
			writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "insufficient balance"})
			return
		}
		h.logger.Error("reserve failed", zap.Int64("user_id", ak.UserID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if req.Stream {
		h.streamGateway(w, r, ak, req, start, estimatedTokens, requestID)
		return
	}

	settleCtx, cancel := settlementContext(r)
	defer cancel()
	chatReq := &upstream.ChatRequest{Model: req.Model, Messages: req.Messages, MaxTokens: req.MaxTokens}
	resp, _, err := h.upstream.Chat(r.Context(), chatReq)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		metrics.UpstreamErrors.WithLabelValues("error").Inc()
		usageID := h.insertUsageLog(r.Context(), ak.UserID, ak.ID, req.Model, 0, 0, 0, 0, latencyMs, http.StatusBadGateway)
		if err := h.billing.Release(settleCtx, ak.UserID, requestID, usageID, "upstream_error"); err != nil {
			h.logger.Error("refund failed", zap.Int64("user_id", ak.UserID), zap.Any("usage_log_id", usageID), zap.Error(err))
		}
		metrics.RequestDuration.WithLabelValues(req.Model, strconv.Itoa(http.StatusBadGateway)).Observe(time.Since(start).Seconds())
		h.logger.Warn("upstream error", zap.Int64("user_id", ak.UserID), zap.String("model", req.Model), zap.Error(err))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	actualTokens := resp.Usage.TotalTokens
	usageID := h.insertUsageLog(r.Context(), ak.UserID, ak.ID, req.Model,
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, resp.Usage.TotalTokens,
		latencyMs, http.StatusOK)
	if err := h.billing.Settle(settleCtx, ak.UserID, requestID, actualTokens, usageID, req.Model); err != nil {
		h.logger.Error("settle failed", zap.Int64("user_id", ak.UserID), zap.Any("usage_log_id", usageID), zap.Error(err))
	}

	metrics.RequestDuration.WithLabelValues(req.Model, strconv.Itoa(http.StatusOK)).Observe(time.Since(start).Seconds())
	h.logger.Info("gateway request completed",
		zap.Int64("user_id", ak.UserID),
		zap.String("model", req.Model),
		zap.Int64("prompt_tokens", resp.Usage.PromptTokens),
		zap.Int64("output_tokens", resp.Usage.CompletionTokens),
		zap.Int64("total_tokens", resp.Usage.TotalTokens),
		zap.Int64("latency_ms", latencyMs),
		zap.Any("usage_log_id", usageID),
	)

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) streamGateway(w http.ResponseWriter, r *http.Request, ak model.APIKey, req gatewayRequest, start time.Time, estimatedTokens int64, requestID string) {
	chatReq := &upstream.ChatRequest{Model: req.Model, Messages: req.Messages, MaxTokens: req.MaxTokens}
	body, _, err := h.upstream.ChatStream(r.Context(), chatReq)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		metrics.UpstreamErrors.WithLabelValues("error").Inc()
		settleCtx, cancel := settlementContext(r)
		defer cancel()
		usageID := h.insertUsageLog(settleCtx, ak.UserID, ak.ID, req.Model, 0, 0, 0, 0, latencyMs, http.StatusBadGateway)
		if refundErr := h.billing.Release(settleCtx, ak.UserID, requestID, usageID, "upstream_error"); refundErr != nil {
			h.logger.Error("refund failed", zap.Int64("user_id", ak.UserID), zap.Any("usage_log_id", usageID), zap.Error(refundErr))
		}
		metrics.RequestDuration.WithLabelValues(req.Model, strconv.Itoa(http.StatusBadGateway)).Observe(time.Since(start).Seconds())
		h.logger.Warn("upstream stream error", zap.Int64("user_id", ak.UserID), zap.String("model", req.Model), zap.Error(err))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	var usage upstream.Usage
	reader := bufio.NewReader(body)
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			parseSSEUsage(line, &usage)
			if _, writeErr := w.Write([]byte(line)); writeErr != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}

	actualTokens := usage.TotalTokens
	if actualTokens <= 0 {
		actualTokens = estimatedTokens
	}
	settleCtx, cancel := settlementContext(r)
	defer cancel()
	usageID := h.insertUsageLog(settleCtx, ak.UserID, ak.ID, req.Model,
		usage.PromptTokens, usage.CompletionTokens, actualTokens, actualTokens, latencyMs, http.StatusOK)
	if err := h.billing.Settle(settleCtx, ak.UserID, requestID, actualTokens, usageID, req.Model); err != nil {
		h.logger.Error("stream settle failed", zap.Int64("user_id", ak.UserID), zap.Any("usage_log_id", usageID), zap.Error(err))
	}
	metrics.RequestDuration.WithLabelValues(req.Model, strconv.Itoa(http.StatusOK)).Observe(time.Since(start).Seconds())
	h.logger.Info("gateway stream completed", zap.Int64("user_id", ak.UserID), zap.String("model", req.Model), zap.Int64("total_tokens", actualTokens), zap.Int64("latency_ms", latencyMs))
}

func settlementContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
}

func parseSSEUsage(line string, usage *upstream.Usage) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return
	}
	var chunk struct {
		Usage *upstream.Usage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err == nil && chunk.Usage != nil {
		*usage = *chunk.Usage
	}
}

func (h *Handler) insertUsageLog(ctx context.Context, userID, apiKeyID int64, modelName string, promptTokens, completionTokens, totalTokens, costTokens, latencyMs int64, statusCode int) *int64 {
	result, err := h.db.ExecContext(ctx,
		"INSERT INTO usage_logs (user_id, api_key_id, model, prompt_tokens, output_tokens, total_tokens, cost_tokens, latency_ms, status_code) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		userID, apiKeyID, modelName, promptTokens, completionTokens, totalTokens, costTokens, latencyMs, statusCode)
	if err != nil {
		h.logger.Error("insert usage log failed", zap.Int64("user_id", userID), zap.Error(err))
		return nil
	}
	usageID, err := result.LastInsertId()
	if err != nil {
		h.logger.Error("read usage log id failed", zap.Int64("user_id", userID), zap.Error(err))
		return nil
	}
	return &usageID
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
