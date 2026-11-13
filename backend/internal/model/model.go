package model

import "time"

type User struct {
	ID           int64     `db:"id" json:"id"`
	Username     string    `db:"username" json:"username"`
	PasswordHash string    `db:"password_hash" json:"-"`
	Role         string    `db:"role" json:"role"`
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
	ID            int64     `db:"id" json:"id"`
	UserID        int64     `db:"user_id" json:"user_id"`
	Type          string    `db:"type" json:"type"`
	Amount        int64     `db:"amount" json:"amount"`
	BalanceAfter  int64     `db:"balance_after" json:"balance_after"`
	UsageLogID    *int64    `db:"usage_log_id" json:"usage_log_id,omitempty"`
	ReferenceType *string   `db:"reference_type" json:"reference_type,omitempty"`
	ReferenceID   *int64    `db:"reference_id" json:"reference_id,omitempty"`
	OperatorID    *int64    `db:"operator_id" json:"operator_id,omitempty"`
	Reason        *string   `db:"reason" json:"reason,omitempty"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

type CreditOperation struct {
	ID              int64     `db:"id" json:"id"`
	OpNo            string    `db:"op_no" json:"op_no"`
	UserID          int64     `db:"user_id" json:"user_id"`
	Kind            string    `db:"kind" json:"kind"`
	DeltaTokens     int64     `db:"delta_tokens" json:"delta_tokens"`
	BalanceBefore   int64     `db:"balance_before" json:"balance_before"`
	BalanceAfter    int64     `db:"balance_after" json:"balance_after"`
	Reason          string    `db:"reason" json:"reason"`
	OperatorID      int64     `db:"operator_id" json:"operator_id"`
	ClientRequestID string    `db:"client_request_id" json:"client_request_id"`
	CreatedAt       time.Time `db:"created_at" json:"created_at"`
}

type AdminAuditLog struct {
	ID         int64     `db:"id" json:"id"`
	ActorID    int64     `db:"actor_id" json:"actor_id"`
	Action     string    `db:"action" json:"action"`
	TargetType string    `db:"target_type" json:"target_type"`
	TargetID   int64     `db:"target_id" json:"target_id"`
	RequestID  *string   `db:"request_id" json:"request_id,omitempty"`
	IP         *string   `db:"ip" json:"ip,omitempty"`
	UserAgent  *string   `db:"user_agent" json:"user_agent,omitempty"`
	Detail     []byte    `db:"detail" json:"detail,omitempty"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

type BalanceHold struct {
	ID        int64      `db:"id" json:"id"`
	RequestID string     `db:"request_id" json:"request_id"`
	UserID    int64      `db:"user_id" json:"user_id"`
	Amount    int64      `db:"amount" json:"amount"`
	Status    string     `db:"status" json:"status"`
	ExpiresAt time.Time  `db:"expires_at" json:"expires_at"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	SettledAt *time.Time `db:"settled_at" json:"settled_at,omitempty"`
}
