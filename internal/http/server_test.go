package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vetka-backend-panel/internal/http/handlers"
)

type stubPinger struct {
	err error
}

func (s stubPinger) Ping(context.Context) error {
	return s.err
}

func TestFormatDateTimeInput(t *testing.T) {
	loc := loadAppLocation("Europe/Moscow")
	value := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC)
	got := formatDateTimeInput(&value, loc)
	if got != "2026-07-11T14:00" {
		t.Fatalf("unexpected datetime-local value: %s", got)
	}
}

func TestFormatDateTime(t *testing.T) {
	loc := loadAppLocation("Europe/Moscow")
	value := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC)
	got := formatDateTime(handlers.LocaleRU, &value, loc)
	if got != "11.07.2026 14:00" {
		t.Fatalf("unexpected formatted datetime: %s", got)
	}
}

func TestReadyHandlerOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	readyHandler(stubPinger{}, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if rec.Body.String() != "ready\n" {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestReadyHandlerDBUnavailable(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	readyHandler(stubPinger{err: errors.New("dial failed")}, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if rec.Body.String() != "database unavailable\n" {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
