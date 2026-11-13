package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/lrs/llm-api-gateway/internal/metrics"
)

type CircuitState int

const (
	CircuitClosed   CircuitState = 0
	CircuitOpen     CircuitState = 1
	CircuitHalfOpen CircuitState = 2
)

var ErrCircuitOpen = fmt.Errorf("circuit breaker is open")

type Client struct {
	baseURL  string
	apiKey   string
	mockMode bool
	http     *http.Client
	logger   *zap.Logger

	mu              sync.Mutex
	state           CircuitState
	failureCount    int
	successCount    int
	lastStateChange time.Time
	maxFailures     int
	openTimeout     time.Duration
}

func NewClient(baseURL, apiKey string, logger *zap.Logger) *Client {
	return &Client{
		baseURL:  baseURL,
		apiKey:   apiKey,
		mockMode: apiKey == "mock",
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:          logger,
		state:           CircuitClosed,
		maxFailures:     5,
		openTimeout:     30 * time.Second,
		lastStateChange: time.Now(),
	}
}

func (c *Client) canPass() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	switch c.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if now.Sub(c.lastStateChange) >= c.openTimeout {
			c.state = CircuitHalfOpen
			c.successCount = 0
			c.lastStateChange = now
			c.logger.Info("circuit half-open, allowing probe request")
			return true
		}
		return false
	case CircuitHalfOpen:
		return true
	default:
		return true
	}
}

func (c *Client) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failureCount = 0
	switch c.state {
	case CircuitHalfOpen:
		c.successCount++
		if c.successCount >= 3 {
			c.state = CircuitClosed
			c.lastStateChange = time.Now()
			c.logger.Info("circuit closed, upstream recovered")
		}
	case CircuitOpen:
		c.state = CircuitClosed
		c.lastStateChange = time.Now()
	}
}

func (c *Client) recordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case CircuitClosed:
		c.failureCount++
		if c.failureCount >= c.maxFailures {
			c.state = CircuitOpen
			c.lastStateChange = time.Now()
			c.logger.Warn("circuit opened",
				zap.Int("failures", c.failureCount),
				zap.Duration("open_timeout", c.openTimeout))
		}
	case CircuitHalfOpen:
		c.state = CircuitOpen
		c.lastStateChange = time.Now()
		c.logger.Warn("circuit re-opened after failed probe")
	}
}

func (c *Client) State() CircuitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

type ChatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

func (c *Client) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, http.Header, error) {
	if !c.canPass() {
		metrics.UpstreamErrors.WithLabelValues("circuit_open").Inc()
		return nil, nil, ErrCircuitOpen
	}
	if len(req.Messages) == 0 {
		return nil, nil, fmt.Errorf("messages is required")
	}

	if c.mockMode {
		contentLen := int64(len(req.Messages[0].Content))/4 + 1
		c.recordSuccess()
		return &ChatResponse{
			ID:     "mock-resp",
			Object: "chat.completion",
			Model:  req.Model,
			Choices: []Choice{{
				Index:        0,
				Message:      Message{Role: "assistant", Content: "Hello! This is a mock response."},
				FinishReason: "stop",
			}},
			Usage: Usage{
				PromptTokens:     contentLen,
				CompletionTokens: 10,
				TotalTokens:      contentLen + 10,
			},
		}, nil, nil
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		c.recordFailure()
		metrics.UpstreamErrors.WithLabelValues("request_error").Inc()
		return nil, nil, fmt.Errorf("upstream request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.recordFailure()
		metrics.UpstreamErrors.WithLabelValues("upstream_error").Inc()
		return nil, resp.Header, fmt.Errorf("upstream error status=%d body=%s", resp.StatusCode, string(body))
	}
	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		c.recordFailure()
		metrics.UpstreamErrors.WithLabelValues("decode_error").Inc()
		return nil, resp.Header, fmt.Errorf("decode response: %w", err)
	}
	c.recordSuccess()
	return &chatResp, resp.Header, nil
}

func (c *Client) ChatStream(ctx context.Context, req *ChatRequest) (io.ReadCloser, http.Header, error) {
	if !c.canPass() {
		metrics.UpstreamErrors.WithLabelValues("circuit_open").Inc()
		return nil, nil, ErrCircuitOpen
	}
	if len(req.Messages) == 0 {
		return nil, nil, fmt.Errorf("messages is required")
	}

	if c.mockMode {
		contentLen := int64(len(req.Messages[0].Content))/4 + 1
		total := contentLen + 10
		stream := fmt.Sprintf(
			"data: {\"id\":\"mock\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello!\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":10,\"total_tokens\":%d}}\n\ndata: [DONE]\n\n",
			req.Model, contentLen, total,
		)
		c.recordSuccess()
		return io.NopCloser(strings.NewReader(stream)), nil, nil
	}

	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		c.recordFailure()
		metrics.UpstreamErrors.WithLabelValues("request_error").Inc()
		return nil, nil, fmt.Errorf("upstream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.recordFailure()
		metrics.UpstreamErrors.WithLabelValues("upstream_error").Inc()
		return nil, resp.Header, fmt.Errorf("upstream error status=%d body=%s", resp.StatusCode, string(body))
	}
	c.recordSuccess()
	return resp.Body, resp.Header, nil
}
