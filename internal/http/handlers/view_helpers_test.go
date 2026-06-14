package handlers

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"vetka-backend-panel/internal/nodes"
	"vetka-backend-panel/internal/users"
)

func TestTruncateTextUnicodeSafe(t *testing.T) {
	value := "Привет, мир"
	got := TruncateText(value, 8)
	if !strings.HasPrefix(got, "Привет") {
		t.Fatalf("unexpected truncation result: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("result must stay valid UTF-8: %q", got)
	}
}

func TestMaskSecretCompact(t *testing.T) {
	if got := MaskSecretCompact("5887abcdefghijklmnopd6f9"); got != "5887••••••••d6f9" {
		t.Fatalf("unexpected compact mask: %q", got)
	}
	if got := MaskSecretCompact("abcd"); got != "••••" {
		t.Fatalf("short values must be fully masked, got %q", got)
	}
}

func TestSubscriptionExpiryTextLocalized(t *testing.T) {
	loc := mustLoadLocation(t, "Europe/Moscow")
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC)

	ru := subscriptionExpiryTextAt(LocaleRU, &expiresAt, loc, now)
	if got, want := ru, "Срок действия до: 11.07.2026 14:00 MSK (Скоро истечёт)"; got != want {
		t.Fatalf("unexpected ru expiry text: got %q want %q", got, want)
	}

	en := subscriptionExpiryTextAt(LocaleEN, &expiresAt, loc, now)
	if got, want := en, "Expires at: 2026-07-11 14:00 MSK (Expires Soon)"; got != want {
		t.Fatalf("unexpected en expiry text: got %q want %q", got, want)
	}
}

func TestFormatNodeStatusFlashLocalizedLabels(t *testing.T) {
	status := nodes.AgentStatusResponse{
		CurrentVersion: 7,
		AppliedVersion: 6,
		UsersCached:    12,
	}

	ru := formatNodeStatusFlash(LocaleRU, status)
	for _, forbidden := range []string{"current_version", "applied_version", "users_cached"} {
		if strings.Contains(ru, forbidden) {
			t.Fatalf("ru flash must not contain machine label %q: %q", forbidden, ru)
		}
	}
	for _, want := range []string{"Текущая версия=7", "Применённая версия=6", "Пользователей в кэше=12"} {
		if !strings.Contains(ru, want) {
			t.Fatalf("ru flash missing %q: %q", want, ru)
		}
	}

	en := formatNodeStatusFlash(LocaleEN, status)
	for _, want := range []string{"Current version=7", "Applied version=6", "Users cached=12"} {
		if !strings.Contains(en, want) {
			t.Fatalf("en flash missing %q: %q", want, en)
		}
	}
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

func TestTimeRemainingLocalized(t *testing.T) {
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		locale   Locale
		expires  *time.Time
		expected string
	}{
		{name: "ru minutes", locale: LocaleRU, expires: ptrTime(now.Add(45 * time.Minute)), expected: "Осталось 45 мин"},
		{name: "en minutes", locale: LocaleEN, expires: ptrTime(now.Add(45 * time.Minute)), expected: "45m remaining"},
		{name: "ru hours", locale: LocaleRU, expires: ptrTime(now.Add(5*time.Hour + 30*time.Minute)), expected: "Осталось 5 ч 30 мин"},
		{name: "en hours", locale: LocaleEN, expires: ptrTime(now.Add(5*time.Hour + 30*time.Minute)), expected: "5h 30m remaining"},
		{name: "ru days", locale: LocaleRU, expires: ptrTime(now.Add(52 * time.Hour)), expected: "Осталось 2 д 4 ч"},
		{name: "en days", locale: LocaleEN, expires: ptrTime(now.Add(52 * time.Hour)), expected: "2d 4h remaining"},
		{name: "ru expired", locale: LocaleRU, expires: ptrTime(now.Add(-time.Minute)), expected: "Срок истёк"},
		{name: "en expired", locale: LocaleEN, expires: ptrTime(now.Add(-time.Minute)), expected: "Expired"},
		{name: "ru unlimited", locale: LocaleRU, expires: nil, expected: "Без ограничений"},
		{name: "en unlimited", locale: LocaleEN, expires: nil, expected: "Unlimited"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := timeRemainingAt(tc.locale, tc.expires, now); got != tc.expected {
				t.Fatalf("unexpected time remaining: got %q want %q", got, tc.expected)
			}
		})
	}
}

func TestSafeJSONPreviewRedactsSecretsRecursively(t *testing.T) {
	payload := []byte(`{"password":"one","nested":{"node_secret":"three"},"items":[{"protocol_password":"four"}],"token":"five","caps":{"ADMIN_API_TOKEN":"six"}}`)
	got := SafeJSONPreview(payload)
	for _, secret := range []string{"one", "three", "four", "five", "six"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q leaked: %s", secret, got)
		}
	}
	for _, needle := range []string{`"password":"***"`, `"node_secret":"***"`, `"protocol_password":"***"`, `"token":"***"`, `"ADMIN_API_TOKEN":"***"`} {
		if !strings.Contains(got, needle) {
			t.Fatalf("expected redacted key %s in %s", needle, got)
		}
	}
}

func TestSafeJSONPreviewInvalidJSON(t *testing.T) {
	if got := SafeJSONPreview([]byte(`{"password":`)); got != "[invalid response JSON]" {
		t.Fatalf("unexpected invalid-json preview: %q", got)
	}
}

func TestSafeOperationalErrorRedactsJSONAndPlainTextSecrets(t *testing.T) {
	cases := []struct {
		name  string
		input string
		leaks []string
		wants []string
	}{
		{
			name:  "json payload",
			input: `{"password":"one","nested":{"node_secret":"three"},"items":[{"protocol_password":"four"}]}`,
			leaks: []string{"one", "three", "four"},
			wants: []string{`"password":"***"`, `"node_secret":"***"`, `"protocol_password":"***"`},
		},
		{
			name:  "plain text bearer and credentials",
			input: `authorization: Bearer abcdef protocol_username=u_demo protocol_password=p_demo`,
			leaks: []string{"abcdef", "u_demo", "p_demo"},
			wants: []string{"authorization: ***", "protocol_username=***", "protocol_password=***"},
		},
		{
			name:  "embedded json-like error text",
			input: `node agent returned 500: {"password":"secret"}`,
			leaks: []string{"secret"},
			wants: []string{`"password":"***"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SafeOperationalError(tc.input)
			for _, leak := range tc.leaks {
				if strings.Contains(got, leak) {
					t.Fatalf("secret %q leaked in %q", leak, got)
				}
			}
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Fatalf("expected %q in %q", want, got)
				}
			}
		})
	}
}

func TestSafeOperationalErrorDoesNotEchoUnsanitizedInvalidSecretPayload(t *testing.T) {
	input := `token`
	got := SafeOperationalError(input)
	if got == input {
		t.Fatalf("expected secret-like text to be redacted, got %q", got)
	}
}

func TestHasDetailedProtocolAccessUsesNodeProtocolType(t *testing.T) {
	access := []users.UserNodeAccessDetail{
		{
			Access: users.Access{
				ProtocolType: "mieru",
				Enabled:      true,
			},
			NodeProtocolType: "naive",
			NodeEnabled:      true,
		},
	}
	if hasDetailedProtocolAccess(access, "mieru") {
		t.Fatal("stale assignment protocol must not grant mieru-only access")
	}
	if !hasDetailedProtocolAccess(access, "naive") {
		t.Fatal("node protocol type should drive protocol-only access visibility")
	}
}

func TestHasDetailedProtocolAccessExcludesDisabledNodes(t *testing.T) {
	access := []users.UserNodeAccessDetail{
		{
			Access: users.Access{
				Enabled: true,
			},
			NodeProtocolType: "mieru",
			NodeEnabled:      false,
		},
	}
	if hasDetailedProtocolAccess(access, "mieru") {
		t.Fatal("disabled nodes must not grant protocol-only access")
	}
}

func TestMakeNodeListItemsSanitizesLastErrorPreview(t *testing.T) {
	raw := `authorization: Bearer abcdef protocol_password=p_demo`
	items := makeNodeListItems(LocaleRU, []nodes.Node{
		{ID: "node-1", Name: "Node One", ProtocolType: "mieru", Enabled: true, SetupState: nodes.SetupStateConnected, LastError: &raw},
	}, map[string]int{"node-1": 1})
	if len(items) != 1 {
		t.Fatalf("expected one item, got %d", len(items))
	}
	if strings.Contains(items[0].LastErrorPreview, "abcdef") || strings.Contains(items[0].LastErrorPreview, "p_demo") {
		t.Fatalf("raw last error leaked in preview: %q", items[0].LastErrorPreview)
	}
	if !strings.Contains(items[0].LastErrorPreview, "***") {
		t.Fatalf("expected redacted preview, got %q", items[0].LastErrorPreview)
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}
