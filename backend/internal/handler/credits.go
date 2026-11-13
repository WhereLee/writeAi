package handler

import (
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/lrs/llm-api-gateway/internal/billing"
	"github.com/lrs/llm-api-gateway/internal/middleware"
	"github.com/lrs/llm-api-gateway/internal/model"
)

type creditChangeReq struct {
	UserID          int64       `json:"user_id"`
	Credits         json.Number `json:"credits"`
	Reason          string      `json:"reason"`
	ClientRequestID string      `json:"client_request_id"`
}

func (h *Handler) GrantCredits(w http.ResponseWriter, r *http.Request) {
	h.applyCreditChange(w, r, false)
}

func (h *Handler) RevokeCredits(w http.ResponseWriter, r *http.Request) {
	h.applyCreditChange(w, r, true)
}

func (h *Handler) applyCreditChange(w http.ResponseWriter, r *http.Request, revoke bool) {
	operatorID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req creditChangeReq
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.UserID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reason is required"})
		return
	}
	tokens, err := h.creditsToTokens(req.Credits)
	if err != nil || tokens <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credits must be a positive number"})
		return
	}
	if h.maxGrantCredits > 0 && tokens > h.maxGrantCredits*h.tokensPerCredit {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credits exceed the per-operation limit"})
		return
	}

	clientRequestID := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if clientRequestID == "" {
		clientRequestID = strings.TrimSpace(req.ClientRequestID)
	}
	if clientRequestID == "" {
		clientRequestID = newUUID()
	}
	kind := "GRANT"
	delta := tokens
	if revoke {
		kind = "REVOKE"
		delta = -tokens
	}

	op, err := h.billing.ApplyCreditChange(r.Context(), billing.CreditChange{
		OperatorID:      operatorID,
		UserID:          req.UserID,
		DeltaTokens:     delta,
		Reason:          reason,
		ClientRequestID: clientRequestID,
		Kind:            kind,
		IP:              clientIP(r),
		UserAgent:       r.UserAgent(),
	})
	if err != nil {
		h.writeCreditError(w, err)
		return
	}
	h.logger.Info("credit change applied",
		zap.Int64("operator_id", operatorID),
		zap.Int64("user_id", op.UserID),
		zap.String("kind", op.Kind),
		zap.Int64("delta_tokens", op.DeltaTokens),
		zap.Int64("balance_after", op.BalanceAfter))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"op_no":           op.OpNo,
		"user_id":         op.UserID,
		"kind":            op.Kind,
		"delta_tokens":    op.DeltaTokens,
		"delta_credits":   h.formatCredits(op.DeltaTokens),
		"balance_tokens":  op.BalanceAfter,
		"balance_credits": h.formatCredits(op.BalanceAfter),
		"reason":          op.Reason,
		"created_at":      op.CreatedAt,
	})
}

func (h *Handler) creditsToTokens(value json.Number) (int64, error) {
	amount, err := strconv.ParseFloat(value.String(), 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return 0, errors.New("invalid credits")
	}
	return int64(math.Round(amount * float64(h.tokensPerCredit))), nil
}

func (h *Handler) writeCreditError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, billing.ErrAccountNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
	case errors.Is(err, billing.ErrNegativeBalance):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "balance would become negative"})
	case errors.Is(err, billing.ErrInvalidAmount), errors.Is(err, billing.ErrInvalidRequestID):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		h.logger.Error("credit operation failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

func (h *Handler) ListCreditOperations(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	conditions := []string{"1 = 1"}
	args := []interface{}{}
	if v := r.URL.Query().Get("user_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			conditions = append(conditions, "user_id = ?")
			args = append(args, n)
		}
	}
	if v := r.URL.Query().Get("operator_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			conditions = append(conditions, "operator_id = ?")
			args = append(args, n)
		}
	}
	args = append(args, limit, offset)
	query := "SELECT id, op_no, user_id, kind, delta_tokens, balance_before, balance_after, reason, operator_id, client_request_id, created_at FROM credit_operations WHERE " +
		strings.Join(conditions, " AND ") + " ORDER BY id DESC LIMIT ? OFFSET ?"
	var rows []model.CreditOperation
	if err := h.db.SelectContext(r.Context(), &rows, query, args...); err != nil {
		h.logger.Error("list credit operations failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) ListAdminUsers(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	like := "%" + search + "%"
	var rows []struct {
		ID        int64     `db:"id" json:"id"`
		Username  string    `db:"username" json:"username"`
		Role      string    `db:"role" json:"role"`
		CreatedAt time.Time `db:"created_at" json:"created_at"`
		Tokens    int64     `db:"tokens" json:"tokens"`
		Credits   string    `json:"credits"`
	}
	query := "SELECT u.id, u.username, u.role, u.created_at, COALESCE(b.balance, 0) AS tokens FROM users u LEFT JOIN balances b ON b.user_id = u.id WHERE (? = '' OR u.username LIKE ?) ORDER BY u.id DESC LIMIT ? OFFSET ?"
	if err := h.db.SelectContext(r.Context(), &rows, query, search, like, limit, offset); err != nil {
		h.logger.Error("list admin users failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	for i := range rows {
		rows[i].Credits = h.formatCredits(rows[i].Tokens)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) ListAuditLogs(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	conditions := []string{"1 = 1"}
	args := []interface{}{}
	if v := r.URL.Query().Get("actor_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			conditions = append(conditions, "actor_id = ?")
			args = append(args, n)
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("action")); v != "" {
		conditions = append(conditions, "action = ?")
		args = append(args, v)
	}
	args = append(args, limit, offset)
	query := "SELECT id, actor_id, action, target_type, target_id, request_id, ip, user_agent, detail, created_at FROM admin_audit_logs WHERE " +
		strings.Join(conditions, " AND ") + " ORDER BY id DESC LIMIT ? OFFSET ?"
	var rows []struct {
		ID         int64           `db:"id" json:"id"`
		ActorID    int64           `db:"actor_id" json:"actor_id"`
		Action     string          `db:"action" json:"action"`
		TargetType string          `db:"target_type" json:"target_type"`
		TargetID   int64           `db:"target_id" json:"target_id"`
		RequestID  *string         `db:"request_id" json:"request_id,omitempty"`
		IP         *string         `db:"ip" json:"ip,omitempty"`
		UserAgent  *string         `db:"user_agent" json:"user_agent,omitempty"`
		Detail     json.RawMessage `db:"detail" json:"detail,omitempty"`
		CreatedAt  time.Time       `db:"created_at" json:"created_at"`
	}
	if err := h.db.SelectContext(r.Context(), &rows, query, args...); err != nil {
		h.logger.Error("list audit logs failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) ListMyLedger(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	limit, offset := pagination(r)
	conditions := []string{"user_id = ?"}
	args := []interface{}{userID}
	if v := strings.TrimSpace(r.URL.Query().Get("type")); v != "" {
		conditions = append(conditions, "type = ?")
		args = append(args, v)
	}
	args = append(args, limit, offset)
	query := "SELECT id, user_id, type, amount, balance_after, usage_log_id, reference_type, reference_id, operator_id, reason, created_at FROM balance_logs WHERE " +
		strings.Join(conditions, " AND ") + " ORDER BY id DESC LIMIT ? OFFSET ?"
	var rows []model.BalanceLog
	if err := h.db.SelectContext(r.Context(), &rows, query, args...); err != nil {
		h.logger.Error("list ledger failed", zap.Int64("user_id", userID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) ListUsage(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	limit, offset := pagination(r)
	var rows []model.UsageLog
	if err := h.db.SelectContext(r.Context(), &rows,
		"SELECT id, user_id, api_key_id, model, prompt_tokens, output_tokens, total_tokens, cost_tokens, latency_ms, status_code, created_at FROM usage_logs WHERE user_id = ? ORDER BY id DESC LIMIT ? OFFSET ?",
		userID, limit, offset); err != nil {
		h.logger.Error("list usage failed", zap.Int64("user_id", userID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func pagination(r *http.Request) (int, int) {
	limit := 20
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

func clientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return strings.TrimSpace(strings.Split(v, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
