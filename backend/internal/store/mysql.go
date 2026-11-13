package store

import (
	"fmt"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

func NewMySQL(dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Connect("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func Migrate(db *sqlx.DB) error {
	queries := []string{
		"CREATE TABLE IF NOT EXISTS users (id BIGINT AUTO_INCREMENT PRIMARY KEY, username VARCHAR(64) NOT NULL UNIQUE, password_hash VARCHAR(255) NOT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS api_keys (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, `key` VARCHAR(64) NULL UNIQUE, key_hash CHAR(64) NULL, key_prefix VARCHAR(16) NULL, name VARCHAR(64) NOT NULL DEFAULT '', is_active BOOLEAN DEFAULT TRUE, expires_at TIMESTAMP NULL, last_used_at TIMESTAMP NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, INDEX idx_user_id (user_id), UNIQUE KEY idx_key_hash (key_hash)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS balances (user_id BIGINT PRIMARY KEY, balance BIGINT NOT NULL DEFAULT 0, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS usage_logs (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, api_key_id BIGINT NOT NULL, model VARCHAR(64) NOT NULL, prompt_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0, total_tokens BIGINT NOT NULL DEFAULT 0, cost_tokens BIGINT NOT NULL DEFAULT 0, latency_ms BIGINT NOT NULL DEFAULT 0, status_code INT NOT NULL DEFAULT 0, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, INDEX idx_user_id (user_id), INDEX idx_created_at (created_at)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS balance_logs (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, type VARCHAR(16) NOT NULL, amount BIGINT NOT NULL, balance_after BIGINT NOT NULL, usage_log_id BIGINT NULL, reference_type VARCHAR(32) NULL, reference_id BIGINT NULL, operator_id BIGINT NULL, reason VARCHAR(255) NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, INDEX idx_user_id (user_id), UNIQUE KEY uk_balance_reference (reference_type, reference_id, type), UNIQUE KEY uk_balance_usage_type (usage_log_id, type), INDEX idx_balance_user_created (user_id, created_at)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS compensations (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, amount BIGINT NOT NULL, usage_log_id BIGINT NULL, reason VARCHAR(16) NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'pending', attempts INT NOT NULL DEFAULT 0, last_error VARCHAR(255) NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, INDEX idx_status (status)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS credit_operations (id BIGINT AUTO_INCREMENT PRIMARY KEY, op_no VARCHAR(40) NOT NULL, user_id BIGINT NOT NULL, kind VARCHAR(16) NOT NULL, delta_tokens BIGINT NOT NULL, balance_before BIGINT NOT NULL, balance_after BIGINT NOT NULL, reason VARCHAR(255) NOT NULL, operator_id BIGINT NOT NULL, client_request_id VARCHAR(64) NOT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, UNIQUE KEY uk_op_no (op_no), UNIQUE KEY uk_operator_request (operator_id, client_request_id), INDEX idx_credit_user_created (user_id, created_at), INDEX idx_credit_operator_created (operator_id, created_at)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS admin_audit_logs (id BIGINT AUTO_INCREMENT PRIMARY KEY, actor_id BIGINT NOT NULL, action VARCHAR(64) NOT NULL, target_type VARCHAR(32) NOT NULL, target_id BIGINT NOT NULL, request_id VARCHAR(64) NULL, ip VARCHAR(64) NULL, user_agent VARCHAR(255) NULL, detail JSON NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, INDEX idx_audit_actor_created (actor_id, created_at), INDEX idx_audit_target (target_type, target_id)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS balance_holds (id BIGINT AUTO_INCREMENT PRIMARY KEY, request_id VARCHAR(64) NOT NULL, user_id BIGINT NOT NULL, amount BIGINT NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'HELD', expires_at TIMESTAMP NOT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, settled_at TIMESTAMP NULL, UNIQUE KEY uk_hold_request (user_id, request_id), INDEX idx_hold_expire (status, expires_at)) ENGINE=InnoDB",
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	if err := migrateAPIKeys(db); err != nil {
		return err
	}
	return migrateCredits(db)
}

func migrateCredits(db *sqlx.DB) error {
	if err := ensureColumn(db, "users", "role", "VARCHAR(16) NOT NULL DEFAULT 'user'"); err != nil {
		return err
	}
	if err := ensureIndex(db, "users", "idx_users_role", "INDEX idx_users_role (role)"); err != nil {
		return err
	}
	columns := []struct {
		name       string
		definition string
	}{
		{"reference_type", "VARCHAR(32) NULL"},
		{"reference_id", "BIGINT NULL"},
		{"operator_id", "BIGINT NULL"},
		{"reason", "VARCHAR(255) NULL"},
	}
	for _, column := range columns {
		if err := ensureColumn(db, "balance_logs", column.name, column.definition); err != nil {
			return err
		}
	}
	indexes := []struct {
		name       string
		definition string
	}{
		{"uk_balance_reference", "UNIQUE KEY uk_balance_reference (reference_type, reference_id, type)"},
		{"idx_balance_user_created", "INDEX idx_balance_user_created (user_id, created_at)"},
	}
	for _, index := range indexes {
		if err := ensureIndex(db, "balance_logs", index.name, index.definition); err != nil {
			return err
		}
	}
	var duplicates int
	if err := db.Get(&duplicates, "SELECT COUNT(*) FROM (SELECT usage_log_id, type FROM balance_logs WHERE usage_log_id IS NOT NULL GROUP BY usage_log_id, type HAVING COUNT(*) > 1) t"); err != nil {
		return err
	}
	if duplicates > 0 {
		fmt.Fprintf(os.Stderr, "warning: balance_logs has %d duplicate (usage_log_id, type) groups; uk_balance_usage_type was not created\n", duplicates)
		return backfillOpeningBalances(db)
	}
	if err := ensureIndex(db, "balance_logs", "uk_balance_usage_type", "UNIQUE KEY uk_balance_usage_type (usage_log_id, type)"); err != nil {
		return err
	}
	return backfillOpeningBalances(db)
}

// backfillOpeningBalances makes SUM(balance_logs.amount) equal balances.balance
// for users that were created before the MySQL-ledger migration. The migration
// row is the difference between the snapshot and the old logs, so it can be
// negative. It is written once per user and never updated afterwards.
func backfillOpeningBalances(db *sqlx.DB) error {
	var rows []struct {
		UserID  int64 `db:"user_id"`
		Balance int64 `db:"balance"`
		Ledger  int64 `db:"ledger"`
	}
	err := db.Select(&rows, `
		SELECT b.user_id, b.balance, COALESCE(SUM(l.amount), 0) AS ledger
		FROM balances b
		LEFT JOIN balance_logs l ON l.user_id = b.user_id
		WHERE NOT EXISTS (
			SELECT 1 FROM balance_logs m
			WHERE m.user_id = b.user_id AND m.reference_type = 'migration' AND m.reference_id = b.user_id
		)
		GROUP BY b.user_id, b.balance`)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range rows {
		delta := row.Balance - row.Ledger
		if delta == 0 {
			continue
		}
		if _, err := tx.Exec(
			"INSERT INTO balance_logs (user_id, type, amount, balance_after, reference_type, reference_id, reason) VALUES (?, 'opening', ?, ?, 'migration', ?, '账本迁移开账')",
			row.UserID, delta, row.Balance, row.UserID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrateAPIKeys(db *sqlx.DB) error {
	columns := []struct {
		name       string
		definition string
	}{
		{"key_hash", "CHAR(64) NULL"},
		{"key_prefix", "VARCHAR(16) NULL"},
		{"expires_at", "TIMESTAMP NULL"},
		{"last_used_at", "TIMESTAMP NULL"},
	}
	for _, column := range columns {
		if err := ensureColumn(db, "api_keys", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := ensureIndex(db, "api_keys", "idx_key_hash", "UNIQUE KEY idx_key_hash (key_hash)"); err != nil {
		return err
	}
	if _, err := db.Exec("UPDATE api_keys SET key_hash = SHA2(`key`, 256), key_prefix = LEFT(`key`, 12) WHERE key_hash IS NULL AND `key` IS NOT NULL"); err != nil {
		return err
	}
	if _, err := db.Exec("ALTER TABLE api_keys MODIFY `key` VARCHAR(64) NULL"); err != nil {
		return err
	}
	if _, err := db.Exec("UPDATE api_keys SET `key` = NULL WHERE key_hash IS NOT NULL AND `key` IS NOT NULL"); err != nil {
		return err
	}
	return nil
}

func ensureColumn(db *sqlx.DB, table, column, definition string) error {
	var count int
	if err := db.Get(&count, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", table, column); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := db.Exec("ALTER TABLE `" + table + "` ADD COLUMN `" + column + "` " + definition)
	return err
}

func ensureIndex(db *sqlx.DB, table, index, definition string) error {
	var count int
	if err := db.Get(&count, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", table, index); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := db.Exec("ALTER TABLE `" + table + "` ADD " + definition)
	return err
}
