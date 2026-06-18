package nodes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func telemetryTestNode(apiURL string) Node {
	return Node{
		ID:           "node-db-1",
		NodeID:       "vistula-naive-1",
		Name:         "Vistula",
		APIURL:       apiURL,
		ProtocolType: "naive",
		NodeSecret:   "ns_secret_value",
	}
}

func TestAgentClientTelemetrySessionsUsesExpectedPathAndHeaders(t *testing.T) {
	t.Run("active", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.String(); got != "/v1/telemetry/sessions" {
				t.Fatalf("unexpected path: %s", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer ns_secret_value" {
				t.Fatalf("unexpected auth header: %q", got)
			}
			if got := r.Header.Get("X-Node-Id"); got != "vistula-naive-1" {
				t.Fatalf("unexpected node id header: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"node_id":"vistula-naive-1","protocol_type":"naive","collector_status":"ok","warnings":[],"sessions":[]}`))
		}))
		defer server.Close()

		client := NewAgentClient()
		resp, _, err := client.TelemetrySessions(context.Background(), telemetryTestNode(server.URL), false)
		if err != nil {
			t.Fatalf("TelemetrySessions returned error: %v", err)
		}
		if !resp.OK || resp.CollectorStatus != "ok" {
			t.Fatalf("unexpected telemetry response: %+v", resp)
		}
	})

	t.Run("recent", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.String(); got != "/v1/telemetry/sessions?include_recent=true" {
				t.Fatalf("unexpected path: %s", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"node_id":"vistula-naive-1","protocol_type":"naive","collector_status":"partial","warnings":["collector lag"],"sessions":[{"protocol":"naive","client_ip":"198.51.100.10","active":true}]}`))
		}))
		defer server.Close()

		client := NewAgentClient()
		resp, _, err := client.TelemetrySessions(context.Background(), telemetryTestNode(server.URL), true)
		if err != nil {
			t.Fatalf("TelemetrySessions returned error: %v", err)
		}
		if resp.CollectorStatus != "partial" || len(resp.Warnings) != 1 {
			t.Fatalf("unexpected telemetry response: %+v", resp)
		}
		if len(resp.Sessions) != 1 || resp.Sessions[0].ClientIP == nil || *resp.Sessions[0].ClientIP != "198.51.100.10" {
			t.Fatalf("unexpected session decode: %+v", resp.Sessions)
		}
	})
}

func TestAgentClientTelemetrySessionsDecodesMieruNullIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"node_id":"mieru-node-1","protocol_type":"mieru","collector_status":"ok","sessions":[{"protocol":"mieru","client_ip":null,"active":true}]}`))
	}))
	defer server.Close()

	client := NewAgentClient()
	node := telemetryTestNode(server.URL)
	node.NodeID = "mieru-node-1"
	node.ProtocolType = "mieru"

	resp, _, err := client.TelemetrySessions(context.Background(), node, false)
	if err != nil {
		t.Fatalf("TelemetrySessions returned error: %v", err)
	}
	if len(resp.Sessions) != 1 {
		t.Fatalf("expected one session, got %d", len(resp.Sessions))
	}
	if resp.Sessions[0].ClientIP != nil {
		t.Fatalf("mieru client_ip must decode as nil, got %+v", resp.Sessions[0].ClientIP)
	}
}

func TestAgentClientTelemetrySessionsControlledErrors(t *testing.T) {
	t.Run("malformed json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true`))
		}))
		defer server.Close()

		_, _, err := NewAgentClient().TelemetrySessions(context.Background(), telemetryTestNode(server.URL), false)
		if err == nil || !strings.Contains(err.Error(), "decode telemetry response") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})

	t.Run("http status does not leak secret", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"password":"super-secret"}`, http.StatusUnauthorized)
		}))
		defer server.Close()

		_, _, err := NewAgentClient().TelemetrySessions(context.Background(), telemetryTestNode(server.URL), false)
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !strings.Contains(got, "http 401") || strings.Contains(got, "super-secret") || strings.Contains(got, "ns_secret_value") {
			t.Fatalf("unexpected controlled error: %q", got)
		}
	})

	t.Run("node mismatch", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true,"node_id":"other-node","protocol_type":"naive","collector_status":"ok","sessions":[]}`))
		}))
		defer server.Close()

		_, _, err := NewAgentClient().TelemetrySessions(context.Background(), telemetryTestNode(server.URL), false)
		if err == nil || !strings.Contains(err.Error(), "node_id mismatch") {
			t.Fatalf("expected node mismatch error, got %v", err)
		}
	})

	t.Run("protocol mismatch", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true,"node_id":"vistula-naive-1","protocol_type":"mieru","collector_status":"ok","sessions":[]}`))
		}))
		defer server.Close()

		_, _, err := NewAgentClient().TelemetrySessions(context.Background(), telemetryTestNode(server.URL), false)
		if err == nil || !strings.Contains(err.Error(), "protocol_type mismatch") {
			t.Fatalf("expected protocol mismatch error, got %v", err)
		}
	})
}
