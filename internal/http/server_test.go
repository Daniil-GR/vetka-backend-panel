package http

import (
	"testing"
	"time"
)

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
	got := formatDateTime(&value, loc)
	if got != "2026-07-11 14:00 MSK" {
		t.Fatalf("unexpected formatted datetime: %s", got)
	}
}
