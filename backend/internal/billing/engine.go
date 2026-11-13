package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/lrs/llm-api-gateway/internal/metrics"
	"github.com/lrs/llm-api-gateway/internal/model"
)

const (
	defaultHoldTTL       = time.Hour
	defaultSweepInterval = 5 * time.Minute
)

type Engine struct {
	db            *sqlx.DB
	logger        *zap.Logger
	holdTTL       time.Duration
	sweepInterval time.Duration
	stopCh        chan struct{}
	wg            sync.WaitGroup
	closeOnce     sync.Once
}

func NewEngine(db *sqlx.DB, logger *zap.Logger) *Engine {
	return NewEngineWithOptions(db, logger, defaultHoldTTL, defaultSweepInterval)
}

func NewEngineWithOptions(db *sqlx.DB, logger *zap.Logger, holdTTL, sweepInterval time.Duration) *Engine {
	e := &Engine{
		db:            db,
		logger:        logger,
		holdTTL:       holdTTL,
		sweepInterval: sweepInterval,
		stopCh:        make(chan struct{}),
	}
	e.wg.Add(1)
	go e.sweepWorker()
	return e
}

func (e *Engine) sweepWorker() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			e.SweepExpiredHolds(context.Background())
		case <-e.stopCh:
			return
		}
	}
}

func (e *Engine) Close(ctx context.Context) {
	e.closeOnce.Do(func() { close(e.stopCh) })
	e.wg.Wait()
}

func (e *Engine) CheckBalance(ctx context.Context, userID int64) (int64, error) {
	timer := prometheus.NewTimer(metrics.BalanceCheckDuration)
	defer timer.ObserveDuration()

	var balance int64
	err := e.db.GetContext(ctx, &balance, "SELECT balance FROM balances WHERE user_id = ?", userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return balance, err
}

func (e *Engine) ensureBalanceRow(ctx context.Context, userID int64) error {
	_, err := e.db.ExecContext(ctx, "INSERT IGNORE INTO balances (user_id, balance) VALUES (?, 0)", userID)
	return err
}

// ReserveWithIdempotency locks the balance row, deducts the estimate and
// records a hold. The hold is what makes settlement and release idempotent.
func (e *Engine) ReserveWithIdempotency(ctx context.Context, userID, estimatedTokens int64, requestID string) error {
	if requestID == "" {
		return ErrInvalidRequestID
	}
	if estimatedTokens <= 0 {
		estimatedTokens = 1
	}
	if err := e.ensureBalanceRow(ctx, userID); err != nil {
		return err
	}

	tx, err := e.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var balance int64
	if err := tx.GetContext(ctx, &balance, "SELECT balance FROM balances WHERE user_id = ? FOR UPDATE", userID); err != nil {
		return err
	}
	if balance < estimatedTokens {
		metrics.InsufficientBalance.Inc()
		return ErrInsufficientBalance
	}
	if _, err := tx.ExecContext(ctx, "UPDATE balances SET balance = balance - ? WHERE user_id = ?", estimatedTokens, userID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO balance_holds (request_id, user_id, amount, status, expires_at) VALUES (?, ?, ?, 'HELD', DATE_ADD(NOW(), INTERVAL ? SECOND))",
		requestID, userID, estimatedTokens, int64(e.holdTTL.Seconds()))
	if err != nil {
		if isDuplicateKey(err) {
			return ErrDuplicateRequest
		}
		return err
	}
	return tx.Commit()
}

// Settle converts a hold into a consume entry. A repeated call is a no-op.
func (e *Engine) Settle(ctx context.Context, userID int64, requestID string, actualTokens int64, usageID *int64, modelName string) error {
	if requestID == "" {
		return ErrInvalidRequestID
	}
	if actualTokens < 0 {
		actualTokens = 0
	}

	tx, err := e.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var balance int64
	if err := tx.GetContext(ctx, &balance, "SELECT balance FROM balances WHERE user_id = ? FOR UPDATE", userID); err != nil {
		return err
	}
	var hold struct {
		ID     int64  `db:"id"`
		Amount int64  `db:"amount"`
		Status string `db:"status"`
	}
	err = tx.GetContext(ctx, &hold,
		"SELECT id, amount, status FROM balance_holds WHERE user_id = ? AND request_id = ? FOR UPDATE",
		userID, requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrHoldNotFound
	}
	if err != nil {
		return err
	}
	if hold.Status != "HELD" {
		return nil
	}

	delta := hold.Amount - actualTokens
	newBalance := balance + delta
	if _, err := tx.ExecContext(ctx, "UPDATE balances SET balance = ? WHERE user_id = ?", newBalance, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE balance_holds SET status = 'SETTLED', settled_at = NOW() WHERE id = ?", hold.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO balance_logs (user_id, type, amount, balance_after, usage_log_id, reference_type, reference_id, reason) VALUES (?, 'consume', ?, ?, ?, 'balance_hold', ?, ?)",
		userID, -actualTokens, newBalance, usageID, hold.ID, "usage_settle"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	metrics.TokensConsumed.WithLabelValues(modelName).Add(float64(actualTokens))
	return nil
}

// Release returns a held amount to the user. A repeated call is a no-op.
func (e *Engine) Release(ctx context.Context, userID int64, requestID string, usageID *int64, reason string) error {
	if requestID == "" {
		return ErrInvalidRequestID
	}
	if reason == "" {
		reason = "released"
	}

	tx, err := e.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var balance int64
	if err := tx.GetContext(ctx, &balance, "SELECT balance FROM balances WHERE user_id = ? FOR UPDATE", userID); err != nil {
		return err
	}
	var hold struct {
		ID     int64  `db:"id"`
		Amount int64  `db:"amount"`
		Status string `db:"status"`
	}
	err = tx.GetContext(ctx, &hold,
		"SELECT id, amount, status FROM balance_holds WHERE user_id = ? AND request_id = ? FOR UPDATE",
		userID, requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrHoldNotFound
	}
	if err != nil {
		return err
	}
	if hold.Status != "HELD" {
		return nil
	}

	newBalance := balance + hold.Amount
	if _, err := tx.ExecContext(ctx, "UPDATE balances SET balance = ? WHERE user_id = ?", newBalance, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE balance_holds SET status = 'RELEASED', settled_at = NOW() WHERE id = ?", hold.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO balance_logs (user_id, type, amount, balance_after, usage_log_id, reference_type, reference_id, reason) VALUES (?, 'refund', ?, ?, ?, 'balance_hold', ?, ?)",
		userID, hold.Amount, newBalance, usageID, hold.ID, truncate(reason, 255)); err != nil {
		return err
	}
	return tx.Commit()
}

type CreditChange struct {
	OperatorID      int64
	UserID          int64
	DeltaTokens     int64
	Reason          string
	ClientRequestID string
	Kind            string
	IP              string
	UserAgent       string
}

// ApplyCreditChange writes the operation, balance and audit log in one
// transaction. Repeating the same client request returns the original op.
func (e *Engine) ApplyCreditChange(ctx context.Context, in CreditChange) (*model.CreditOperation, error) {
	if in.DeltaTokens == 0 {
		return nil, ErrInvalidAmount
	}
	if in.ClientRequestID == "" {
		return nil, ErrInvalidRequestID
	}
	if in.Kind == "" {
		if in.DeltaTokens > 0 {
			in.Kind = "GRANT"
		} else {
			in.Kind = "REVOKE"
		}
	}

	var exists int64
	if err := e.db.GetContext(ctx, &exists, "SELECT id FROM users WHERE id = ?", in.UserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAccountNotFound
		}
		return nil, err
	}
	if err := e.ensureBalanceRow(ctx, in.UserID); err != nil {
		return nil, err
	}

	tx, err := e.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var balance int64
	if err := tx.GetContext(ctx, &balance, "SELECT balance FROM balances WHERE user_id = ? FOR UPDATE", in.UserID); err != nil {
		return nil, err
	}
	if in.DeltaTokens < 0 && balance+in.DeltaTokens < 0 {
		return nil, ErrNegativeBalance
	}

	before := balance
	after := balance + in.DeltaTokens
	opNo := newOpNo()
	result, err := tx.ExecContext(ctx,
		"INSERT INTO credit_operations (op_no, user_id, kind, delta_tokens, balance_before, balance_after, reason, operator_id, client_request_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		opNo, in.UserID, in.Kind, in.DeltaTokens, before, after, truncate(in.Reason, 255), in.OperatorID, in.ClientRequestID)
	if err != nil {
		if isDuplicateKey(err) {
			tx.Rollback()
			return e.operationByRequest(ctx, in.OperatorID, in.ClientRequestID)
		}
		return nil, err
	}
	opID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE balances SET balance = ? WHERE user_id = ?", after, in.UserID); err != nil {
		return nil, err
	}
	logType := strings.ToLower(in.Kind)
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO balance_logs (user_id, type, amount, balance_after, reference_type, reference_id, operator_id, reason) VALUES (?, ?, ?, ?, 'credit_operation', ?, ?, ?)",
		in.UserID, logType, in.DeltaTokens, after, opID, in.OperatorID, truncate(in.Reason, 255)); err != nil {
		return nil, err
	}

	detail, _ := json.Marshal(map[string]interface{}{
		"op_no":          opNo,
		"kind":           in.Kind,
		"delta_tokens":   in.DeltaTokens,
		"balance_before": before,
		"balance_after":  after,
		"reason":         in.Reason,
	})
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO admin_audit_logs (actor_id, action, target_type, target_id, request_id, ip, user_agent, detail) VALUES (?, ?, 'user', ?, ?, ?, ?, ?)",
		in.OperatorID, "credit."+logType, in.UserID, in.ClientRequestID, truncate(in.IP, 64), truncate(in.UserAgent, 255), detail); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &model.CreditOperation{
		ID:              opID,
		OpNo:            opNo,
		UserID:          in.UserID,
		Kind:            in.Kind,
		DeltaTokens:     in.DeltaTokens,
		BalanceBefore:   before,
		BalanceAfter:    after,
		Reason:          in.Reason,
		OperatorID:      in.OperatorID,
		ClientRequestID: in.ClientRequestID,
		CreatedAt:       time.Now(),
	}, nil
}

func (e *Engine) operationByRequest(ctx context.Context, operatorID int64, requestID string) (*model.CreditOperation, error) {
	var op model.CreditOperation
	err := e.db.GetContext(ctx, &op,
		"SELECT id, op_no, user_id, kind, delta_tokens, balance_before, balance_after, reason, operator_id, client_request_id, created_at FROM credit_operations WHERE operator_id = ? AND client_request_id = ?",
		operatorID, requestID)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

func (e *Engine) SweepExpiredHolds(ctx context.Context) {
	var rows []struct {
		UserID    int64  `db:"user_id"`
		RequestID string `db:"request_id"`
	}
	if err := e.db.SelectContext(ctx, &rows,
		"SELECT user_id, request_id FROM balance_holds WHERE status = 'HELD' AND expires_at < NOW() ORDER BY id LIMIT 100"); err != nil {
		e.logger.Error("load expired holds failed", zap.Error(err))
		return
	}
	for _, row := range rows {
		if err := e.Release(ctx, row.UserID, row.RequestID, nil, "hold_expired"); err != nil && !errors.Is(err, ErrHoldNotFound) {
			e.logger.Error("release expired hold failed",
				zap.Int64("user_id", row.UserID),
				zap.String("request_id", row.RequestID),
				zap.Error(err))
		}
	}
}

func (e *Engine) Reconcile(ctx context.Context, userID int64) (int64, error) {
	var balance int64
	if err := e.db.GetContext(ctx, &balance, "SELECT balance FROM balances WHERE user_id = ?", userID); err != nil {
		return 0, err
	}
	var ledger int64
	if err := e.db.GetContext(ctx, &ledger, "SELECT COALESCE(SUM(amount), 0) FROM balance_logs WHERE user_id = ?", userID); err != nil {
		return 0, err
	}
	return balance - ledger, nil
}

func newOpNo() string {
	return "CR" + time.Now().Format("20060102150405") + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
