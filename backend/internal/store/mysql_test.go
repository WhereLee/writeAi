package store

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func storeTestDB(t *testing.T) *sqlx.DB {
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
	require.NoError(t, Migrate(db))
	t.Cleanup(func() { db.Close() })
	return db
}

func TestBackfillOpeningBalances(t *testing.T) {
	db := storeTestDB(t)
	username := fmt.Sprintf("store_%d", time.Now().UnixNano())
	result, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?, 'x')", username)
	require.NoError(t, err)
	userID, err := result.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID)
		db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
		db.Exec("DELETE FROM users WHERE id = ?", userID)
	})

	tx, err := db.Beginx()
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO balances (user_id, balance) VALUES (?, 1000)", userID)
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO balance_logs (user_id, type, amount, balance_after, reason) VALUES (?, 'consume', 1200, 1000, 'old mismatch')", userID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	require.NoError(t, Migrate(db))

	var sum int64
	require.NoError(t, db.Get(&sum, "SELECT COALESCE(SUM(amount), 0) FROM balance_logs WHERE user_id = ?", userID))
	require.Equal(t, int64(1000), sum)

	var migrationRows int
	require.NoError(t, db.Get(&migrationRows, "SELECT COUNT(*) FROM balance_logs WHERE user_id = ? AND reference_type = 'migration'", userID))
	require.Equal(t, 1, migrationRows)

	require.NoError(t, Migrate(db))
	require.NoError(t, db.Get(&migrationRows, "SELECT COUNT(*) FROM balance_logs WHERE user_id = ? AND reference_type = 'migration'", userID))
	require.Equal(t, 1, migrationRows)
}

func TestBackfillSkipsAlreadyConsistentLedger(t *testing.T) {
	db := storeTestDB(t)
	username := fmt.Sprintf("store_ok_%d", time.Now().UnixNano())
	result, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?, 'x')", username)
	require.NoError(t, err)
	userID, err := result.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Exec("DELETE FROM balance_logs WHERE user_id = ?", userID)
		db.Exec("DELETE FROM balances WHERE user_id = ?", userID)
		db.Exec("DELETE FROM users WHERE id = ?", userID)
	})

	tx, err := db.Beginx()
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO balances (user_id, balance) VALUES (?, 1000)", userID)
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO balance_logs (user_id, type, amount, balance_after, reference_type, reference_id, reason) VALUES (?, 'grant', 1000, 1000, 'registration', ?, '注册赠送')", userID, userID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	require.NoError(t, Migrate(db))

	var sum int64
	require.NoError(t, db.Get(&sum, "SELECT COALESCE(SUM(amount), 0) FROM balance_logs WHERE user_id = ?", userID))
	require.Equal(t, int64(1000), sum)

	var migrationRows int
	require.NoError(t, db.Get(&migrationRows, "SELECT COUNT(*) FROM balance_logs WHERE user_id = ? AND reference_type = 'migration'", userID))
	require.Zero(t, migrationRows)
}
