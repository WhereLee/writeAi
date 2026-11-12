package upstream

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

func TestMockModeRejectsEmptyMessages(t *testing.T) {
	client := NewClient("http://example.invalid", "mock", zap.NewNop())
	_, _, err := client.Chat(context.Background(), &ChatRequest{Model: "test"})
	if err == nil {
		t.Fatal("expected an error for empty messages")
	}
}

func TestMockModeReturnsUsage(t *testing.T) {
	client := NewClient("http://example.invalid", "mock", zap.NewNop())
	resp, _, err := client.Chat(context.Background(), &ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "hello world"}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Usage.TotalTokens == 0 {
		t.Fatal("expected non-zero usage")
	}
}
