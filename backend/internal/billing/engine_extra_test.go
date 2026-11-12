package billing

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestReserveLoadsBalanceFromDB(t *testing.T) {
	db := getTestDB(t)
	const userID int64 = 900001
	db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
	_, err := db.Exec("INSERT INTO balances (user_id, balance) VALUES (?, 500)", userID)
	require.NoError(t, err)
	t.Cleanup(func() { db.Exec("DELETE FROM balances WHERE user_id = ?", userID) })

	mr := miniredis.RunT(t)
	rdb := newTestRedisClient(mr.Addr())
	engine := NewEngine(rdb, db, zap.NewNop())

	require.NoError(t, engine.Reserve(context.Background(), userID, 100))
	balance, err := rdb.Get(context.Background(), "balance:900001").Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(400), balance)
}

func TestReserveConcurrentCacheMiss(t *testing.T) {
	db := getTestDB(t)
	const userID int64 = 900006
	db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
	_, err := db.Exec("INSERT INTO balances (user_id, balance) VALUES (?, 1000)", userID)
	require.NoError(t, err)
	t.Cleanup(func() { db.Exec("DELETE FROM balances WHERE user_id = ?", userID) })

	mr := miniredis.RunT(t)
	rdb := newTestRedisClient(mr.Addr())
	engine := NewEngine(rdb, db, zap.NewNop())

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, engine.Reserve(context.Background(), userID, 10))
		}()
	}
	wg.Wait()

	balance, err := rdb.Get(context.Background(), "balance:900006").Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(900), balance)
}

func TestReserveWithIdempotencyIsUserScoped(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := newTestRedisClient(mr.Addr())
	engine := NewEngine(rdb, getTestDB(t), zap.NewNop())

	mr.Set("balance:900002", "1000")
	require.NoError(t, engine.ReserveWithIdempotency(context.Background(), 900002, 10, "req-1"))
	err := engine.ReserveWithIdempotency(context.Background(), 900002, 10, "req-1")
	assert.ErrorIs(t, err, ErrDuplicateRequest)

	mr.Set("balance:900003", "1000")
	require.NoError(t, engine.ReserveWithIdempotency(context.Background(), 900003, 10, "req-1"))
}

func TestReserveWithIdempotencyConcurrent(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := newTestRedisClient(mr.Addr())
	engine := NewEngine(rdb, getTestDB(t), zap.NewNop())
	mr.Set("balance:900004", "100000")

	var wg sync.WaitGroup
	var successes atomic.Int64
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := engine.ReserveWithIdempotency(context.Background(), 900004, 10, "same-request"); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(1), successes.Load())
}

func TestSettleLinksUsageLog(t *testing.T) {
	db := getTestDB(t)
	const userID int64 = 900005
	result, err := db.Exec(
		"INSERT INTO usage_logs (user_id, api_key_id, model, prompt_tokens, output_tokens, total_tokens, cost_tokens, latency_ms, status_code) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		userID, 1, "test", 1, 2, 3, 3, 10, 200,
	)
	require.NoError(t, err)
	usageID, err := result.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID)
		db.Exec("DELETE FROM usage_logs WHERE user_id = ?", userID)
	})

	mr := miniredis.RunT(t)
	rdb := newTestRedisClient(mr.Addr())
	mr.Set("balance:900005", "1000")
	engine := NewEngine(rdb, db, zap.NewNop())

	require.NoError(t, engine.Settle(context.Background(), userID, 10, 3, &usageID, "test"))
	var got int64
	require.NoError(t, db.Get(&got, "SELECT usage_log_id FROM balance_logs WHERE user_id = ? ORDER BY id DESC LIMIT 1", userID))
	assert.Equal(t, usageID, got)
}

func TestSettleWithoutUsageLogWritesNull(t *testing.T) {
	db := getTestDB(t)
	const userID int64 = 900007
	t.Cleanup(func() { db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID) })

	mr := miniredis.RunT(t)
	rdb := newTestRedisClient(mr.Addr())
	mr.Set("balance:900007", "100")
	engine := NewEngine(rdb, db, zap.NewNop())

	require.NoError(t, engine.Settle(context.Background(), userID, 10, 3, nil, "test"))
	var got int
	require.NoError(t, db.Get(&got, "SELECT COUNT(*) FROM balance_logs WHERE user_id = ? AND usage_log_id IS NULL", userID))
	assert.Equal(t, 1, got)
}
