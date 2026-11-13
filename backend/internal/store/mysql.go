package store

import (
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
		"CREATE TABLE IF NOT EXISTS plans (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL, token_amount BIGINT NOT NULL, price_cents BIGINT NOT NULL, discount_rate DOUBLE DEFAULT 1.0) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS orders (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, plan_id BIGINT NOT NULL, amount_cents BIGINT NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'pending', created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, paid_at TIMESTAMP NULL, INDEX idx_user_id (user_id), INDEX idx_status (status)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS usage_logs (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, api_key_id BIGINT NOT NULL, model VARCHAR(64) NOT NULL, prompt_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0, total_tokens BIGINT NOT NULL DEFAULT 0, cost_tokens BIGINT NOT NULL DEFAULT 0, latency_ms BIGINT NOT NULL DEFAULT 0, status_code INT NOT NULL DEFAULT 0, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, INDEX idx_user_id (user_id), INDEX idx_created_at (created_at)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS balance_logs (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, type VARCHAR(16) NOT NULL, amount BIGINT NOT NULL, balance_after BIGINT NOT NULL, usage_log_id BIGINT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, INDEX idx_user_id (user_id)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS compensations (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, amount BIGINT NOT NULL, usage_log_id BIGINT NULL, reason VARCHAR(16) NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'pending', attempts INT NOT NULL DEFAULT 0, last_error VARCHAR(255) NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, INDEX idx_status (status)) ENGINE=InnoDB",
		"INSERT IGNORE INTO plans (id, name, token_amount, price_cents, discount_rate) VALUES (1, 'Starter', 100000, 990, 1.0), (2, 'Pro', 1000000, 8990, 0.9), (3, 'Enterprise', 10000000, 79900, 0.8)",
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return migrateAPIKeys(db)
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
