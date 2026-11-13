package handler

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
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
	db               *sqlx.DB
	billing          *billing.Engine
	upstream         *upstream.Client
	rateLimiter      *middleware.RateLimiter
	jwtSecret        string
	logger           *zap.Logger
	rateLimitPerMin  int
	allowMockPayment bool
}

func NewHandler(db *sqlx.DB, be *billing.Engine, uc *upstream.Client, rl *middleware.RateLimiter, jwtSecret string, logger *zap.Logger, rateLimitPerMin int, allowMockPayment bool) *Handler {
	return &Handler{db: db, billing: be, upstream: uc, rateLimiter: rl, jwtSecret: jwtSecret, logger: logger, rateLimitPerMin: rateLimitPerMin, allowMockPayment: allowMockPayment}
}

type registerReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
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
	hash, err := hashPassword(req.Password)
	if err != nil {
		h.logger.Error("hash password failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	result, err := h.db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", req.Username, hash)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "username already exists"})
		return
	}
	userID, _ := result.LastInsertId()
	h.db.Exec("INSERT INTO balances (user_id, balance) VALUES (?, 100000)", userID)
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
	err := h.db.Get(&user, "SELECT id, username, password_hash FROM users WHERE username = ?", req.Username)
	if err != nil {
		h.logger.Warn("login failed", zap.String("username", req.Username), zap.Error(err))
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	if !checkPassword(req.Password, user.PasswordHash) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	token, err := generateJWT(user.ID, h.jwtSecret)
	if err != nil {
		h.logger.Error("generate jwt failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"token": token, "user_id": user.ID})
}

type createKeyReq struct {
	Name string `json:"name"`
}

func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req createKeyReq
	json.NewDecoder(r.Body).Decode(&req)
	key := "sk-" + newUUID()[:32]
	result, err := h.db.Exec("INSERT INTO api_keys (user_id, `key`, name) VALUES (?, ?, ?)", userID, key, req.Name)
	if err != nil {
		h.logger.Error("create api key failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	keyID, _ := result.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": keyID, "key": key, "name": req.Name})
}

func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var keys []model.APIKey
	err := h.db.Select(&keys, "SELECT id, user_id, `key`, name, is_active, created_at FROM api_keys WHERE user_id = ?", userID)
	if err != nil {
		h.logger.Error("list api keys failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, keys)
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
	writeJSON(w, http.StatusOK, map[string]interface{}{"user_id": userID, "balance": bal})
}

func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	var plans []model.Plan
	err := h.db.Select(&plans, "SELECT id, name, token_amount, price_cents, discount_rate FROM plans")
	if err != nil {
		h.logger.Error("list plans failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, plans)
}

type createOrderReq struct {
	PlanID int64 `json:"plan_id"`
}

func (h *Handler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req createOrderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PlanID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan_id is required"})
		return
	}

	var plan model.Plan
	if err := h.db.GetContext(r.Context(), &plan, "SELECT id, name, token_amount, price_cents, discount_rate FROM plans WHERE id = ?", req.PlanID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "plan not found"})
		return
	}

	amountCents := int64(math.Round(float64(plan.PriceCents) * plan.DiscountRate))
	result, err := h.db.ExecContext(r.Context(), "INSERT INTO orders (user_id, plan_id, amount_cents, status) VALUES (?, ?, ?, 'pending')", userID, plan.ID, amountCents)
	if err != nil {
		h.logger.Error("create order failed", zap.Int64("user_id", userID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	orderID, _ := result.LastInsertId()

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id": orderID, "plan_id": plan.ID, "plan_name": plan.Name,
		"token_amount": plan.TokenAmount, "amount_cents": amountCents, "status": "pending",
	})
}

func (h *Handler) ListOrders(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var orders []model.Order
	err := h.db.SelectContext(r.Context(), &orders,
		"SELECT id, user_id, plan_id, amount_cents, status, created_at, paid_at FROM orders WHERE user_id = ? ORDER BY id DESC",
		userID)
	if err != nil {
		h.logger.Error("list orders failed", zap.Int64("user_id", userID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, orders)
}

func (h *Handler) PayOrder(w http.ResponseWriter, r *http.Request) {
	if !h.allowMockPayment {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "mock payment is disabled"})
		return
	}

	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	orderID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid order id"})
		return
	}

	var order model.Order
	if err := h.db.GetContext(r.Context(), &order, "SELECT id, user_id, plan_id, amount_cents, status FROM orders WHERE id = ? AND user_id = ?", orderID, userID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "order not found"})
		return
	}
	if order.Status != "pending" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "order is not pending"})
		return
	}

	var plan model.Plan
	if err := h.db.GetContext(r.Context(), &plan, "SELECT id, token_amount FROM plans WHERE id = ?", order.PlanID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "plan not found"})
		return
	}

	claim, err := h.db.ExecContext(r.Context(), "UPDATE orders SET status = 'paying' WHERE id = ? AND user_id = ? AND status = 'pending'", order.ID, userID)
	if err != nil {
		h.logger.Error("claim order failed", zap.Int64("order_id", order.ID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if affected, _ := claim.RowsAffected(); affected == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "order is not pending"})
		return
	}

	if err := h.billing.Recharge(r.Context(), userID, plan.TokenAmount); err != nil {
		h.db.ExecContext(r.Context(), "UPDATE orders SET status = 'failed' WHERE id = ?", order.ID)
		h.logger.Error("recharge failed", zap.Int64("order_id", order.ID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "recharge failed"})
		return
	}
	if _, err := h.db.ExecContext(r.Context(), "UPDATE orders SET status = 'paid', paid_at = NOW() WHERE id = ?", order.ID); err != nil {
		h.logger.Error("mark order paid failed", zap.Int64("order_id", order.ID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "order update failed"})
		return
	}

	balance, _ := h.billing.CheckBalance(r.Context(), userID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"order_id": order.ID, "status": "paid", "token_amount": plan.TokenAmount, "balance": balance,
	})
}

type gatewayRequest struct {
	Model    string             `json:"model"`
	Messages []upstream.Message `json:"messages"`
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
	err := h.db.Get(&ak, "SELECT id, user_id, `key`, is_active FROM api_keys WHERE `key` = ? AND is_active = TRUE", apiKey)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or inactive API key"})
		return
	}

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

	if err := h.billing.ReserveWithIdempotency(r.Context(), ak.UserID, estimatedTokens, r.Header.Get("X-Request-Id")); err != nil {
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

	chatReq := &upstream.ChatRequest{Model: req.Model, Messages: req.Messages}
	resp, _, err := h.upstream.Chat(r.Context(), chatReq)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		metrics.UpstreamErrors.WithLabelValues("error").Inc()
		usageID := h.insertUsageLog(r.Context(), ak.UserID, ak.ID, req.Model, 0, 0, 0, 0, latencyMs, http.StatusBadGateway)
		if err := h.billing.RefundWithCompensation(r.Context(), ak.UserID, estimatedTokens, usageID); err != nil {
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
	if err := h.billing.Settle(r.Context(), ak.UserID, estimatedTokens, actualTokens, usageID, req.Model); err != nil {
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
