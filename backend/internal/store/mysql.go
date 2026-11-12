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
		"CREATE TABLE IF NOT EXISTS api_keys (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, `key` VARCHAR(64) NOT NULL UNIQUE, name VARCHAR(64) NOT NULL DEFAULT '', is_active BOOLEAN DEFAULT TRUE, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, INDEX idx_user_id (user_id)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS balances (user_id BIGINT PRIMARY KEY, balance BIGINT NOT NULL DEFAULT 0, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS plans (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL, token_amount BIGINT NOT NULL, price_cents BIGINT NOT NULL, discount_rate DOUBLE DEFAULT 1.0) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS orders (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, plan_id BIGINT NOT NULL, amount_cents BIGINT NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'pending', created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, paid_at TIMESTAMP NULL, INDEX idx_user_id (user_id), INDEX idx_status (status)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS usage_logs (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, api_key_id BIGINT NOT NULL, model VARCHAR(64) NOT NULL, prompt_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0, total_tokens BIGINT NOT NULL DEFAULT 0, cost_tokens BIGINT NOT NULL DEFAULT 0, latency_ms BIGINT NOT NULL DEFAULT 0, status_code INT NOT NULL DEFAULT 0, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, INDEX idx_user_id (user_id), INDEX idx_created_at (created_at)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS balance_logs (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, type VARCHAR(16) NOT NULL, amount BIGINT NOT NULL, balance_after BIGINT NOT NULL, usage_log_id BIGINT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, INDEX idx_user_id (user_id)) ENGINE=InnoDB",
		"INSERT IGNORE INTO plans (id, name, token_amount, price_cents, discount_rate) VALUES (1, 'Starter', 100000, 990, 1.0), (2, 'Pro', 1000000, 8990, 0.9), (3, 'Enterprise', 10000000, 79900, 0.8)",
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
