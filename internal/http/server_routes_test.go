package http

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"vetka-backend-panel/internal/config"
)

func TestNodeDetailRouteExists(t *testing.T) {
	app := NewServer(config.Config{
		AppEnv:                          "test",
		AdminUsername:                   "admin",
		AdminPassword:                   "secret",
		AdminAPIToken:                   "token",
		AppTimezone:                     "Europe/Moscow",
		SubscriptionProfileTitle:        "Vetka VPN",
		SubscriptionUpdateIntervalHours: 12,
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req := httptest.NewRequest(http.MethodGet, "/nodes/node-1", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected protected route redirect, got %d", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "/login" {
		t.Fatalf("expected redirect to /login, got %q", location)
	}
}
