package billing

import "errors"

var (
	ErrInsufficientBalance = errors.New("insufficient balance")
	ErrUpstreamTimeout     = errors.New("upstream timeout")
	ErrUpstreamError       = errors.New("upstream error")
	ErrRateLimited         = errors.New("rate limited")
	ErrInvalidAPIKey       = errors.New("invalid or inactive API key")
	ErrDuplicateRequest    = errors.New("duplicate request")
)
