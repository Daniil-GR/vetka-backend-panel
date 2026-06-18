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

func (c *AgentClient) StatusInfo(ctx context.Context, node Node) (AgentStatusResponse, AgentCallResult, error) {
	result, err := c.Status(ctx, node)
	if err != nil {
		return AgentStatusResponse{}, result, err
	}
	var status AgentStatusResponse
	if err := json.Unmarshal(result.Body, &status); err != nil {
		return AgentStatusResponse{}, result, fmt.Errorf("decode /status response: %w", err)
	}
	return status, result, nil
}

func (c *AgentClient) Stats(ctx context.Context, node Node) (AgentCallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.do(ctx, node, http.MethodGet, "/v1/stats", nil)
}

func (c *AgentClient) TelemetrySessions(ctx context.Context, node Node, includeRecent bool) (TelemetryResponse, AgentCallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	path := "/v1/telemetry/sessions"
	if includeRecent {
		path += "?include_recent=true"
	}

	result, err := c.do(ctx, node, http.MethodGet, path, nil)
	if err != nil {
		if result.StatusCode > 0 {
			return TelemetryResponse{}, result, fmt.Errorf("telemetry request failed with http %d", result.StatusCode)
		}
		return TelemetryResponse{}, result, fmt.Errorf("telemetry request failed")
	}

	var response TelemetryResponse
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return TelemetryResponse{}, result, fmt.Errorf("decode telemetry response: %w", err)
	}
	if !response.OK {
		return TelemetryResponse{}, result, fmt.Errorf("telemetry response returned ok=false")
	}
	if response.NodeID != "" && response.NodeID != node.NodeID {
		return TelemetryResponse{}, result, fmt.Errorf("telemetry node_id mismatch")
	}
	if response.ProtocolType != "" && response.ProtocolType != node.ProtocolType {
		return TelemetryResponse{}, result, fmt.Errorf("telemetry protocol_type mismatch")
	}
	return response, result, nil
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
