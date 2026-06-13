package handlers

import (
	"strings"
	"testing"
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
	items := makeNodeListItems([]nodes.Node{
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
