package handlers

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestParseDateTimeMoscow(t *testing.T) {
	loc := loadAppLocation("Europe/Moscow")
	got := parseDateTime("2026-07-11T14:00", loc)
	if got == nil {
		t.Fatal("parseDateTime returned nil")
	}
	if got.Location().String() != "Europe/Moscow" {
		t.Fatalf("unexpected location: %s", got.Location())
	}
	if got.Hour() != 14 || got.Minute() != 0 {
		t.Fatalf("unexpected wall clock time: %s", got.In(loc).Format(time.RFC3339))
	}
	if got.UTC().Unix() != time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("unexpected unix timestamp: %d", got.UTC().Unix())
	}
}

func TestUserInputFromFormReadsQuotaAndExactDatetime(t *testing.T) {
	form := url.Values{
		"username":   {"demo"},
		"expires_at": {"2026-07-11T14:00"},
		"quota_mb":   {"1024"},
		"enabled":    {"true"},
	}
	req := httptest.NewRequest("POST", "/users", nil)
	req.Form = form
	h := &Handler{appLocation: loadAppLocation("Europe/Moscow")}

	got := h.userInputFromForm(req)
	if got.QuotaMB != 1024 {
		t.Fatalf("unexpected quota_mb: %d", got.QuotaMB)
	}
	if got.ExpiresAt == nil {
		t.Fatal("expected expires_at to be parsed")
	}
	if got.ExpiresAt.In(h.appLocation).Format("2006-01-02 15:04 MST") != "2026-07-11 14:00 MSK" {
		t.Fatalf("unexpected expires_at: %s", got.ExpiresAt.In(h.appLocation).Format("2006-01-02 15:04 MST"))
	}
}
