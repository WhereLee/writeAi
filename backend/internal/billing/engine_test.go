package billing

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestEngine(t *testing.T) (*Engine, *miniredis.Miniredis) {
	mr := miniredis.RunT(t)
	rdb := newTestRedisClient(mr.Addr())
	db := getTestDB(t)
	engine := NewEngine(rdb, db, zap.NewNop())
	return engine, mr
}

func TestReserveConcurrent(t *testing.T) {
	engine, mr := newTestEngine(t)
	mr.Set("balance:1", "50")

	var wg sync.WaitGroup
	var successes int64
	var mutex sync.Mutex

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := engine.Reserve(ctx, 1, 10); err == nil {
				mutex.Lock()
				successes++
				mutex.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(5), successes)
	bal, _ := mr.Get("balance:1")
	assert.Equal(t, "0", bal)
}

func TestReserveInsufficientBalance(t *testing.T) {
	engine, mr := newTestEngine(t)
	mr.Set("balance:1", "5")

	err := engine.Reserve(context.Background(), 1, 10)
	assert.Error(t, err)
	assert.Equal(t, ErrInsufficientBalance, err)

	bal, _ := mr.Get("balance:1")
	assert.Equal(t, "5", bal)
}

func TestRefund(t *testing.T) {
	engine, mr := newTestEngine(t)
	mr.Set("balance:1", "0")

	err := engine.Refund(context.Background(), 1, 100, 0)
	require.NoError(t, err)

	bal, _ := mr.Get("balance:1")
	assert.Equal(t, "100", bal)
}

func TestRecharge(t *testing.T) {
	engine, mr := newTestEngine(t)
	mr.Set("balance:1", "100")

	err := engine.Recharge(context.Background(), 1, 50)
	require.NoError(t, err)

	bal, _ := mr.Get("balance:1")
	assert.Equal(t, "150", bal)
}
