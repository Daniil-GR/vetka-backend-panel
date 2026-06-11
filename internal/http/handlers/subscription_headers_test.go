package handlers

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"vetka-backend-panel/internal/config"
	"vetka-backend-panel/internal/users"
)

func TestApplySubscriptionHeaders(t *testing.T) {
	h := &Handler{
		cfg: config.Config{
			SubscriptionProfileTitle:        "Ветка VPN",
			SubscriptionUpdateIntervalHours: 12,
		},
	}

	cases := []struct {
		format      string
		contentType string
		filename    string
	}{
		{format: "", contentType: "application/json; charset=utf-8", filename: `attachment; filename="vetka-vpn.json"`},
		{format: "hiddify", contentType: "text/plain; charset=utf-8", filename: `attachment; filename="vetka-vpn.txt"`},
		{format: "mierus", contentType: "text/plain; charset=utf-8", filename: `attachment; filename="vetka-vpn.txt"`},
		{format: "naive", contentType: "text/plain; charset=utf-8", filename: `attachment; filename="vetka-vpn.txt"`},
		{format: "raw", contentType: "text/plain; charset=utf-8", filename: `attachment; filename="vetka-vpn.txt"`},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.applySubscriptionHeaders(rec, tc.format, tc.contentType, users.User{})
		if got := rec.Header().Get("Profile-Title"); got != "base64:0JLQtdGC0LrQsCBWUE4=" {
			t.Fatalf("unexpected Profile-Title for %q: %s", tc.format, got)
		}
		if got := rec.Header().Get("Profile-Update-Interval"); got != "12" {
			t.Fatalf("unexpected Profile-Update-Interval for %q: %s", tc.format, got)
		}
		if got := rec.Header().Get("Subscription-Userinfo"); got != "upload=0; download=0; total=0; expire=0" {
			t.Fatalf("unexpected Subscription-Userinfo for %q: %s", tc.format, got)
		}
		if got := rec.Header().Get("Content-Disposition"); got != tc.filename {
			t.Fatalf("unexpected Content-Disposition for %q: %s", tc.format, got)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.contentType {
			t.Fatalf("unexpected Content-Type for %q: %s", tc.format, got)
		}
	}
}

func TestSubscriptionUserinfo(t *testing.T) {
	expires := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	withQuota := subscriptionUserinfo(users.User{ExpiresAt: &expires, QuotaMB: 1024})
	expected := "upload=0; download=0; total=1073741824; expire=" + strconv.FormatInt(expires.UTC().Unix(), 10)
	if withQuota != expected {
		t.Fatalf("unexpected userinfo with quota/expiry: %s", withQuota)
	}

	noExpiry := subscriptionUserinfo(users.User{})
	if noExpiry != "upload=0; download=0; total=0; expire=0" {
		t.Fatalf("unexpected userinfo without expiry: %s", noExpiry)
	}
}
