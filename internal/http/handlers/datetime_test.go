package handlers

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseOptionalDateTimeMoscow(t *testing.T) {
	loc := loadAppLocation("Europe/Moscow")
	got, err := parseOptionalDateTime("2026-07-11T14:00", loc)
	if err != nil {
		t.Fatalf("parseOptionalDateTime returned error: %v", err)
	}
	if got == nil {
		t.Fatal("parseOptionalDateTime returned nil")
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

	got, err := h.userInputFromForm(req)
	if err != nil {
		t.Fatalf("userInputFromForm returned error: %v", err)
	}
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

func TestParseOptionalDateTimeRejectsDateOnly(t *testing.T) {
	loc := loadAppLocation("Europe/Moscow")
	got, err := parseOptionalDateTime("2026-07-11", loc)
	if err == nil || got != nil {
		t.Fatalf("date-only input must be rejected, got value=%v err=%v", got, err)
	}
}

func TestUserInputFromFormDoesNotDropTimeToMidnight(t *testing.T) {
	form := url.Values{
		"username":   {"demo"},
		"expires_at": {"2026-07-11T14:00"},
		"enabled":    {"true"},
	}
	req := httptest.NewRequest("POST", "/users", nil)
	req.Form = form
	h := &Handler{appLocation: loadAppLocation("Europe/Moscow")}

	got, err := h.userInputFromForm(req)
	if err != nil {
		t.Fatalf("userInputFromForm returned error: %v", err)
	}
	if got.ExpiresAt == nil {
		t.Fatal("expected expires_at to be parsed")
	}
	if got.ExpiresAt.In(h.appLocation).Hour() != 14 || got.ExpiresAt.In(h.appLocation).Minute() != 0 {
		t.Fatalf("expected 14:00, got %s", got.ExpiresAt.In(h.appLocation).Format("15:04"))
	}
}

func TestUserInputFromFormReturnsErrorForInvalidExpiration(t *testing.T) {
	form := url.Values{
		"username":   {"demo"},
		"expires_at": {"2026-07-11"},
		"enabled":    {"true"},
	}
	req := httptest.NewRequest("POST", "/users", nil)
	req.Form = form
	h := &Handler{appLocation: loadAppLocation("Europe/Moscow")}

	_, err := h.userInputFromForm(req)
	if err == nil {
		t.Fatal("expected invalid expiration error")
	}
}

func TestBoolFromFormHandlesHiddenFalseAndCheckedTrue(t *testing.T) {
	req := httptest.NewRequest("POST", "/nodes", strings.NewReader("enabled=false&enabled=true"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := boolFromForm(req, "enabled", false); !got {
		t.Fatalf("expected true, got false")
	}
}

func TestBoolFromFormUncheckedReturnsFalse(t *testing.T) {
	req := httptest.NewRequest("POST", "/nodes", strings.NewReader("enabled=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := boolFromForm(req, "enabled", true); got {
		t.Fatalf("expected false, got true")
	}
}

func TestBoolFromFormUsesFallbackWhenMissing(t *testing.T) {
	req := httptest.NewRequest("POST", "/nodes", nil)
	if !boolFromForm(req, "enabled", true) {
		t.Fatalf("expected fallback true")
	}
}

func TestNodeInputFromFormUncheckedEnabledIsFalse(t *testing.T) {
	req := httptest.NewRequest("POST", "/nodes/node-1", strings.NewReader("name=Node&node_id=agent-1&domain=example.com&protocol_type=mieru&enabled=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got := nodeInputFromForm(req)
	if got.Enabled {
		t.Fatalf("expected disabled node input")
	}
}

func TestNodeInputFromFormCheckedEnabledIsTrue(t *testing.T) {
	req := httptest.NewRequest("POST", "/nodes/node-1", strings.NewReader("name=Node&node_id=agent-1&domain=example.com&protocol_type=mieru&enabled=false&enabled=true"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got := nodeInputFromForm(req)
	if !got.Enabled {
		t.Fatalf("expected enabled node input")
	}
}

func TestUserInputFromFormUncheckedEnabledIsFalse(t *testing.T) {
	req := httptest.NewRequest("POST", "/users/user-1", strings.NewReader("username=demo&enabled=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h := &Handler{appLocation: loadAppLocation("Europe/Moscow")}
	got, err := h.userInputFromForm(req)
	if err != nil {
		t.Fatalf("userInputFromForm returned error: %v", err)
	}
	if got.Enabled {
		t.Fatalf("expected disabled user input")
	}
}
