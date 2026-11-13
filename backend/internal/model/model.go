package model

import "time"

type User struct {
	ID           int64     `db:"id" json:"id"`
	Username     string    `db:"username" json:"username"`
	PasswordHash string    `db:"password_hash" json:"-"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}

type APIKey struct {
	ID         int64      `db:"id" json:"id"`
	UserID     int64      `db:"user_id" json:"user_id"`
	KeyHash    string     `db:"key_hash" json:"-"`
	KeyPrefix  string     `db:"key_prefix" json:"key_prefix"`
	Name       string     `db:"name" json:"name"`
	IsActive   bool       `db:"is_active" json:"is_active"`
	ExpiresAt  *time.Time `db:"expires_at" json:"expires_at"`
	LastUsedAt *time.Time `db:"last_used_at" json:"last_used_at"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
}

type Balance struct {
	UserID    int64     `db:"user_id" json:"user_id"`
	Balance   int64     `db:"balance" json:"balance"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

type Plan struct {
	ID           int64   `db:"id" json:"id"`
	Name         string  `db:"name" json:"name"`
	TokenAmount  int64   `db:"token_amount" json:"token_amount"`
	PriceCents   int64   `db:"price_cents" json:"price_cents"`
	DiscountRate float64 `db:"discount_rate" json:"discount_rate"`
}

type Order struct {
	ID          int64      `db:"id" json:"id"`
	UserID      int64      `db:"user_id" json:"user_id"`
	PlanID      int64      `db:"plan_id" json:"plan_id"`
	AmountCents int64      `db:"amount_cents" json:"amount_cents"`
	Status      string     `db:"status" json:"status"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	PaidAt      *time.Time `db:"paid_at" json:"paid_at"`
}

type UsageLog struct {
	ID           int64     `db:"id" json:"id"`
	UserID       int64     `db:"user_id" json:"user_id"`
	APIKeyID     int64     `db:"api_key_id" json:"api_key_id"`
	Model        string    `db:"model" json:"model"`
	PromptTokens int64     `db:"prompt_tokens" json:"prompt_tokens"`
	OutputTokens int64     `db:"output_tokens" json:"output_tokens"`
	TotalTokens  int64     `db:"total_tokens" json:"total_tokens"`
	CostTokens   int64     `db:"cost_tokens" json:"cost_tokens"`
	LatencyMs    int64     `db:"latency_ms" json:"latency_ms"`
	StatusCode   int       `db:"status_code" json:"status_code"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}

type BalanceLog struct {
	ID           int64     `db:"id" json:"id"`
	UserID       int64     `db:"user_id" json:"user_id"`
	Type         string    `db:"type" json:"type"`
	Amount       int64     `db:"amount" json:"amount"`
	BalanceAfter int64     `db:"balance_after" json:"balance_after"`
	UsageLogID   *int64    `db:"usage_log_id" json:"usage_log_id"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}
