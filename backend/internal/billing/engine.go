package billing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lrs/llm-api-gateway/internal/metrics"
)

type Engine struct {
	rdb        *redis.Client
	db         *sqlx.DB
	logger     *zap.Logger
	compQueue  chan CompensateItem
	compWg     sync.WaitGroup
	closing    bool
	closeMutex sync.Mutex
}

type CompensateItem struct {
	UserID int64
	Amount int64
	Model  string
	Reason string // refund, settle
}

func NewEngine(rdb *redis.Client, db *sqlx.DB, logger *zap.Logger) *Engine {
	e := &Engine{
		rdb:       rdb,
		db:        db,
		logger:    logger,
		compQueue: make(chan CompensateItem, 1000),
	}
	go e.compensationWorker()
	return e
}

func (e *Engine) compensationWorker() {
	e.compWg.Add(1)
	defer e.compWg.Done()
	for item := range e.compQueue {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		key := fmt.Sprintf("balance:%d", item.UserID)
		if item.Reason == "refund" {
			if err := e.rdb.IncrBy(ctx, key, item.Amount).Err(); err != nil {
				e.logger.Error("compensation refund failed",
					zap.Int64("user_id", item.UserID),
					zap.Int64("amount", item.Amount),
					zap.Error(err))
			} else {
				e.logger.Info("compensation refund completed",
					zap.Int64("user_id", item.UserID),
					zap.Int64("amount", item.Amount))
			}
		}
		cancel()
	}
}

func (e *Engine) ShutdownCompensation() {
	e.closeMutex.Lock()
	if e.closing {
		e.closeMutex.Unlock()
		return
	}
	e.closing = true
	e.closeMutex.Unlock()
	close(e.compQueue)
	e.compWg.Wait()
}

func (e *Engine) CheckBalance(ctx context.Context, userID int64) (int64, error) {
	key := fmt.Sprintf("balance:%d", userID)
	bal, err := e.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		var balance int64
		err = e.db.GetContext(ctx, &balance, "SELECT balance FROM balances WHERE user_id = ?", userID)
		if err != nil {
			return 0, err
		}
		ok, err := e.rdb.SetNX(ctx, key, balance, 0).Result()
		if err != nil {
			return 0, err
		}
		if !ok {
			return e.rdb.Get(ctx, key).Int64()
		}
		return balance, nil
	}
	return bal, err
}

var reserveScript = redis.NewScript(`
	local bal = redis.call('GET', KEYS[1])
	if not bal then bal = 0 end
	bal = tonumber(bal)
	if bal < tonumber(ARGV[1]) then
		return -1
	end
	redis.call('DECRBY', KEYS[1], ARGV[1])
	return bal - tonumber(ARGV[1])
`)

// ReserveWithIdempotency rejects a repeated request ID before reserving tokens.
// The key is scoped by user and expires after 10 minutes.
func (e *Engine) ReserveWithIdempotency(ctx context.Context, userID int64, estimatedTokens int64, requestID string) error {
	if requestID != "" {
		idemKey := fmt.Sprintf("idem:%d:%s", userID, requestID)
		ok, err := e.rdb.SetNX(ctx, idemKey, "1", 10*time.Minute).Result()
		if err != nil {
			return err
		}
		if !ok {
			return ErrDuplicateRequest
		}
		if err := e.Reserve(ctx, userID, estimatedTokens); err != nil {
			e.rdb.Del(ctx, idemKey)
			return err
		}
		return nil
	}
	return e.Reserve(ctx, userID, estimatedTokens)
}

func (e *Engine) Reserve(ctx context.Context, userID int64, estimatedTokens int64) error {
	timer := prometheus.NewTimer(metrics.BalanceCheckDuration)
	defer timer.ObserveDuration()

	if estimatedTokens <= 0 {
		estimatedTokens = 1
	}
	key := fmt.Sprintf("balance:%d", userID)
	exists, err := e.rdb.Exists(ctx, key).Result()
	if err != nil {
		e.logger.Error("balance cache check failed", zap.Int64("user_id", userID), zap.Error(err))
		return err
	}
	if exists == 0 {
		if _, err := e.CheckBalance(ctx, userID); err != nil {
			e.logger.Error("load balance from db failed", zap.Int64("user_id", userID), zap.Error(err))
			return err
		}
	}
	result, err := reserveScript.Run(ctx, e.rdb, []string{key}, estimatedTokens).Int64()
	if err != nil {
		e.logger.Error("reserve failed", zap.Int64("user_id", userID), zap.Error(err))
		return err
	}
	if result < 0 {
		metrics.InsufficientBalance.Inc()
		return ErrInsufficientBalance
	}
	return nil
}

func (e *Engine) Settle(ctx context.Context, userID int64, estimatedTokens, actualTokens int64, usageID *int64, model string) error {
	diff := estimatedTokens - actualTokens
	key := fmt.Sprintf("balance:%d", userID)
	if diff > 0 {
		if err := e.rdb.IncrBy(ctx, key, diff).Err(); err != nil {
			e.logger.Error("settle refund failed", zap.Int64("user_id", userID), zap.Error(err))
			return err
		}
	} else if diff < 0 {
		if err := e.rdb.DecrBy(ctx, key, -diff).Err(); err != nil {
			e.logger.Error("settle charge failed", zap.Int64("user_id", userID), zap.Error(err))
			return err
		}
	}

	metrics.TokensConsumed.WithLabelValues(model).Add(float64(actualTokens))

	tx, err := e.db.BeginTxx(ctx, nil)
	if err != nil {
		e.logger.Error("begin tx failed", zap.Error(err))
		return err
	}
	bal, _ := e.rdb.Get(ctx, key).Int64()
	_, err = tx.ExecContext(ctx, "INSERT INTO balance_logs (user_id, type, amount, balance_after, usage_log_id) VALUES (?, 'consume', ?, ?, ?)",
		userID, -actualTokens, bal, usageID)
	if err != nil {
		tx.Rollback()
		e.logger.Error("balance log insert failed", zap.Int64("user_id", userID), zap.Error(err))
		return err
	}
	return tx.Commit()
}

// RefundWithCompensation refunds tokens synchronously; on failure, enqueues compensation.
func (e *Engine) RefundWithCompensation(ctx context.Context, userID int64, amount int64, usageID *int64) error {
	key := fmt.Sprintf("balance:%d", userID)
	err := e.rdb.IncrBy(ctx, key, amount).Err()
	if err != nil {
		e.logger.Error("sync refund failed, enqueueing compensation",
			zap.Int64("user_id", userID),
			zap.Int64("amount", amount),
			zap.Error(err))
		select {
		case e.compQueue <- CompensateItem{UserID: userID, Amount: amount, Reason: "refund"}:
			e.logger.Info("compensation queued", zap.Int64("user_id", userID), zap.Int64("amount", amount))
		default:
			e.logger.Error("compensation queue full, refund lost",
				zap.Int64("user_id", userID),
				zap.Int64("amount", amount))
		}
		return err
	}

	bal, _ := e.rdb.Get(ctx, key).Int64()
	_, err = e.db.ExecContext(ctx, "INSERT INTO balance_logs (user_id, type, amount, balance_after, usage_log_id) VALUES (?, 'refund', ?, ?, ?)",
		userID, amount, bal, usageID)
	if err != nil {
		e.logger.Error("refund balance log insert failed", zap.Int64("user_id", userID), zap.Error(err))
		return err
	}
	return nil
}

func (e *Engine) Refund(ctx context.Context, userID int64, amount int64, usageID int64) error {
	key := fmt.Sprintf("balance:%d", userID)
	return e.rdb.IncrBy(ctx, key, amount).Err()
}

func (e *Engine) Recharge(ctx context.Context, userID int64, amount int64) error {
	key := fmt.Sprintf("balance:%d", userID)
	exists, err := e.rdb.Exists(ctx, key).Result()
	if err != nil {
		return err
	}
	if exists == 0 {
		var balance int64
		err = e.db.GetContext(ctx, &balance, "SELECT balance FROM balances WHERE user_id = ?", userID)
		if err != nil {
			return err
		}
		if err := e.rdb.SetNX(ctx, key, balance, 0).Err(); err != nil {
			return err
		}
	}
	if err := e.rdb.IncrBy(ctx, key, amount).Err(); err != nil {
		return err
	}

	bal, _ := e.rdb.Get(ctx, key).Int64()
	_, err = e.db.ExecContext(ctx, "INSERT INTO balance_logs (user_id, type, amount, balance_after) VALUES (?, 'recharge', ?, ?)",
		userID, amount, bal)
	return err
}

func (e *Engine) SyncToDB(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("balance:%d", userID)
	bal, err := e.rdb.Get(ctx, key).Int64()
	if err != nil {
		return err
	}
	_, err = e.db.ExecContext(ctx, "UPDATE balances SET balance = ? WHERE user_id = ?", bal, userID)
	if err != nil {
		return err
	}
	e.logger.Info("synced balance to db", zap.Int64("user_id", userID), zap.Int64("balance", bal))
	return nil
}

// Reconcile compares Redis balance with MySQL computed balance for a user.
// Returns the drift (redis - mysql). Zero means consistent.
func (e *Engine) Reconcile(ctx context.Context, userID int64) (int64, error) {
	key := fmt.Sprintf("balance:%d", userID)
	redisBal, err := e.rdb.Get(ctx, key).Int64()
	if err != nil {
		return 0, err
	}

	var mysqlBal int64
	err = e.db.GetContext(ctx, &mysqlBal, "SELECT balance FROM balances WHERE user_id = ?", userID)
	if err != nil {
		return 0, err
	}

	drift := redisBal - mysqlBal
	if drift != 0 {
		e.logger.Warn("balance drift detected",
			zap.Int64("user_id", userID),
			zap.Int64("redis_balance", redisBal),
			zap.Int64("mysql_balance", mysqlBal),
			zap.Int64("drift", drift))
	}
	return drift, nil
}

func (e *Engine) Close(ctx context.Context, activeUserIDs []int64) {
	e.logger.Info("syncing all balances to db before shutdown")
	for _, id := range activeUserIDs {
		if err := e.SyncToDB(ctx, id); err != nil {
			e.logger.Error("failed to sync balance", zap.Int64("user_id", id), zap.Error(err))
		}
	}
	e.ShutdownCompensation()
}
