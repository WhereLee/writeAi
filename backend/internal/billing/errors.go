package billing

import "errors"

var (
	ErrInsufficientBalance = errors.New("insufficient balance")
	ErrUpstreamTimeout     = errors.New("upstream timeout")
	ErrUpstreamError       = errors.New("upstream error")
	ErrRateLimited         = errors.New("rate limited")
	ErrInvalidAPIKey       = errors.New("invalid or inactive API key")
	ErrDuplicateRequest    = errors.New("duplicate request")
	ErrAccountNotFound     = errors.New("account not found")
	ErrHoldNotFound        = errors.New("balance hold not found")
	ErrNegativeBalance     = errors.New("balance would become negative")
	ErrInvalidAmount       = errors.New("invalid amount")
	ErrInvalidRequestID    = errors.New("request id is required")
)
