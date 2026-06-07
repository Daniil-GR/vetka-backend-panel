package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type AgentClient struct {
	client *http.Client
}

func NewAgentClient() *AgentClient {
	return &AgentClient{client: &http.Client{}}
}

type AgentCallResult struct {
	StatusCode int
	Body       []byte
}

func (c *AgentClient) Health(ctx context.Context, node Node) (AgentCallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.do(ctx, node, http.MethodGet, "/health", nil)
}

func (c *AgentClient) Status(ctx context.Context, node Node) (AgentCallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.do(ctx, node, http.MethodGet, "/status", nil)
}

func (c *AgentClient) Stats(ctx context.Context, node Node) (AgentCallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.do(ctx, node, http.MethodGet, "/v1/stats", nil)
}

func (c *AgentClient) Reload(ctx context.Context, node Node) (AgentCallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.do(ctx, node, http.MethodPost, "/v1/reload", []byte(`{}`))
}

func (c *AgentClient) Sync(ctx context.Context, node Node, payload SyncPayload) (AgentCallResult, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return AgentCallResult{}, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := c.do(ctx, node, http.MethodPost, "/v1/sync", body)
	return result, body, err
}

func (c *AgentClient) do(ctx context.Context, node Node, method, path string, body []byte) (AgentCallResult, error) {
	base := strings.TrimRight(node.APIURL, "/")
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return AgentCallResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+node.NodeSecret)
	req.Header.Set("X-Node-Id", node.NodeID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return AgentCallResult{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return AgentCallResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return AgentCallResult{StatusCode: resp.StatusCode, Body: respBody}, fmt.Errorf("node agent returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return AgentCallResult{StatusCode: resp.StatusCode, Body: respBody}, nil
}
