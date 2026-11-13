package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestEngine(t *testing.T, holdTTL, sweepInterval time.Duration) *Engine {
	t.Helper()
	engine := NewEngineWithOptions(getTestDB(t), zap.NewNop(), holdTTL, sweepInterval)
	t.Cleanup(func() { engine.Close(context.Background()) })
	return engine
}

func createUser(t *testing.T, engine *Engine, balance int64) int64 {
	t.Helper()
	username := fmt.Sprintf("test_%d", time.Now().UnixNano())
	result, err := engine.db.Exec("INSERT INTO users (username, password_hash) VALUES (?, 'x')", username)
	require.NoError(t, err)
	userID, err := result.LastInsertId()
	require.NoError(t, err)
	_, err = engine.db.Exec("INSERT INTO balances (user_id, balance) VALUES (?, ?)", userID, balance)
	require.NoError(t, err)
	if balance > 0 {
		_, err = engine.db.Exec(
			"INSERT INTO balance_logs (user_id, type, amount, balance_after, reference_type, reference_id, reason) VALUES (?, 'opening', ?, ?, 'test_opening', ?, 'test')",
			userID, balance, balance, userID)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		engine.db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID)
		engine.db.Exec("DELETE FROM balance_holds WHERE user_id = ?", userID)
		engine.db.Exec("DELETE FROM credit_operations WHERE user_id = ? OR operator_id = ?", userID, userID)
		engine.db.Exec("DELETE FROM admin_audit_logs WHERE target_id = ? OR actor_id = ?", userID, userID)
		engine.db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
		engine.db.Exec("DELETE FROM users WHERE id = ?", userID)
	})
	return userID
}

func TestReserveAndSettle(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	userID := createUser(t, engine, 1000)
	ctx := context.Background()

	require.NoError(t, engine.ReserveWithIdempotency(ctx, userID, 100, "req-1"))
	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(900), balance)

	require.NoError(t, engine.Settle(ctx, userID, "req-1", 30, nil, "test"))
	balance, err = engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(970), balance)

	var status string
	require.NoError(t, engine.db.Get(&status, "SELECT status FROM balance_holds WHERE user_id = ? AND request_id = ?", userID, "req-1"))
	assert.Equal(t, "SETTLED", status)

	drift, err := engine.Reconcile(ctx, userID)
	require.NoError(t, err)
	assert.Zero(t, drift)
}

func TestReserveInsufficientBalance(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	userID := createUser(t, engine, 5)
	ctx := context.Background()

	err := engine.ReserveWithIdempotency(ctx, userID, 10, "req-insufficient")
	assert.ErrorIs(t, err, ErrInsufficientBalance)
	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(5), balance)
}

func TestReserveDuplicateRequest(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	userID := createUser(t, engine, 1000)
	ctx := context.Background()

	require.NoError(t, engine.ReserveWithIdempotency(ctx, userID, 100, "same"))
	err := engine.ReserveWithIdempotency(ctx, userID, 100, "same")
	assert.ErrorIs(t, err, ErrDuplicateRequest)
	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(900), balance)
}

func TestReserveConcurrent(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	userID := createUser(t, engine, 1000)
	ctx := context.Background()

	var wg sync.WaitGroup
	var successes atomic.Int64
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := engine.ReserveWithIdempotency(ctx, userID, 10, fmt.Sprintf("concurrent-%d", n)); err == nil {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, int64(20), successes.Load())

	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(800), balance)
}

func TestSettleAfterReleaseIsNoop(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	userID := createUser(t, engine, 1000)
	ctx := context.Background()

	require.NoError(t, engine.ReserveWithIdempotency(ctx, userID, 100, "released-first"))
	require.NoError(t, engine.Release(ctx, userID, "released-first", nil, "upstream_error"))
	require.NoError(t, engine.Settle(ctx, userID, "released-first", 30, nil, "test"))

	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), balance)
}

func TestSettleIsIdempotent(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	userID := createUser(t, engine, 1000)
	ctx := context.Background()

	require.NoError(t, engine.ReserveWithIdempotency(ctx, userID, 100, "settle-once"))
	require.NoError(t, engine.Settle(ctx, userID, "settle-once", 30, nil, "test"))
	require.NoError(t, engine.Settle(ctx, userID, "settle-once", 30, nil, "test"))

	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(970), balance)

	var logs int
	require.NoError(t, engine.db.Get(&logs, "SELECT COUNT(*) FROM balance_logs WHERE user_id = ? AND type = 'consume'", userID))
	assert.Equal(t, 1, logs)
}

func TestReleaseReturnsHeldAmount(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	userID := createUser(t, engine, 1000)
	ctx := context.Background()

	require.NoError(t, engine.ReserveWithIdempotency(ctx, userID, 100, "release-once"))
	require.NoError(t, engine.Release(ctx, userID, "release-once", nil, "upstream_error"))
	require.NoError(t, engine.Release(ctx, userID, "release-once", nil, "upstream_error"))

	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), balance)

	var logs int
	require.NoError(t, engine.db.Get(&logs, "SELECT COUNT(*) FROM balance_logs WHERE user_id = ? AND type = 'refund'", userID))
	assert.Equal(t, 1, logs)
}

func TestSweepExpiredHolds(t *testing.T) {
	engine := newTestEngine(t, 0, time.Hour)
	userID := createUser(t, engine, 1000)
	ctx := context.Background()

	require.NoError(t, engine.ReserveWithIdempotency(ctx, userID, 100, "expire-me"))
	time.Sleep(1100 * time.Millisecond)
	engine.SweepExpiredHolds(ctx)

	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), balance)

	var status string
	require.NoError(t, engine.db.Get(&status, "SELECT status FROM balance_holds WHERE user_id = ? AND request_id = ?", userID, "expire-me"))
	assert.Equal(t, "RELEASED", status)
}

func TestGrantIsIdempotent(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	operatorID := createUser(t, engine, 0)
	userID := createUser(t, engine, 0)
	ctx := context.Background()

	first, err := engine.ApplyCreditChange(ctx, CreditChange{
		OperatorID:      operatorID,
		UserID:          userID,
		DeltaTokens:     100000,
		Reason:          "内测额度",
		ClientRequestID: "grant-1",
		Kind:            "GRANT",
	})
	require.NoError(t, err)
	second, err := engine.ApplyCreditChange(ctx, CreditChange{
		OperatorID:      operatorID,
		UserID:          userID,
		DeltaTokens:     100000,
		Reason:          "内测额度",
		ClientRequestID: "grant-1",
		Kind:            "GRANT",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(100000), balance)

	var logs int
	require.NoError(t, engine.db.Get(&logs, "SELECT COUNT(*) FROM admin_audit_logs WHERE target_id = ?", userID))
	assert.Equal(t, 1, logs)
}

func TestGrantConcurrentSameRequest(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	operatorID := createUser(t, engine, 0)
	userID := createUser(t, engine, 0)
	ctx := context.Background()

	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := engine.ApplyCreditChange(ctx, CreditChange{
				OperatorID:      operatorID,
				UserID:          userID,
				DeltaTokens:     100000,
				Reason:          "并发发放",
				ClientRequestID: "grant-concurrent",
				Kind:            "GRANT",
			})
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 10, successes)

	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(100000), balance)
}

func TestRevokeCannotMakeBalanceNegative(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	operatorID := createUser(t, engine, 0)
	userID := createUser(t, engine, 100)
	ctx := context.Background()

	_, err := engine.ApplyCreditChange(ctx, CreditChange{
		OperatorID:      operatorID,
		UserID:          userID,
		DeltaTokens:     -200,
		Reason:          "扣减过多",
		ClientRequestID: "revoke-too-much",
		Kind:            "REVOKE",
	})
	assert.ErrorIs(t, err, ErrNegativeBalance)

	balance, err := engine.CheckBalance(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(100), balance)

	var ops int
	require.NoError(t, engine.db.Get(&ops, "SELECT COUNT(*) FROM credit_operations WHERE user_id = ?", userID))
	assert.Zero(t, ops)
}

func TestGrantUnknownUser(t *testing.T) {
	engine := newTestEngine(t, time.Hour, time.Hour)
	operatorID := createUser(t, engine, 0)

	_, err := engine.ApplyCreditChange(context.Background(), CreditChange{
		OperatorID:      operatorID,
		UserID:          99999999,
		DeltaTokens:     100,
		Reason:          "不存在",
		ClientRequestID: "grant-missing",
		Kind:            "GRANT",
	})
	assert.True(t, errors.Is(err, ErrAccountNotFound))
}
