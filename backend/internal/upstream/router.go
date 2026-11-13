package upstream

import (
	"context"
	"io"
	"net/http"

	"go.uber.org/zap"
)

type Route struct {
	URL string
	Key string
}

type Router struct {
	defaultClient *Client
	clients       map[string]*Client
}

func NewRouter(defaultURL, defaultKey string, routes map[string]Route, logger *zap.Logger) *Router {
	r := &Router{
		defaultClient: NewClient(defaultURL, defaultKey, logger),
		clients:       make(map[string]*Client, len(routes)),
	}
	for model, route := range routes {
		r.clients[model] = NewClient(route.URL, route.Key, logger)
	}
	return r
}

func (r *Router) clientFor(model string) *Client {
	if client, ok := r.clients[model]; ok {
		return client
	}
	return r.defaultClient
}

func (r *Router) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, http.Header, error) {
	return r.clientFor(req.Model).Chat(ctx, req)
}

func (r *Router) ChatStream(ctx context.Context, req *ChatRequest) (io.ReadCloser, http.Header, error) {
	return r.clientFor(req.Model).ChatStream(ctx, req)
}
