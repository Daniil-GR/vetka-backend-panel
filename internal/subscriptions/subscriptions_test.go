package subscriptions

import (
	"strings"
	"testing"

	"vetka-backend-panel/internal/users"
)

func TestBuildNaiveURI(t *testing.T) {
	uri := BuildNaiveURI(users.AccessWithNode{
		Access:                   users.Access{ProtocolUsername: "demo", ProtocolPassword: "secret"},
		NodeName:                 "Node One",
		NodeDomain:               "example.com",
		NodeProtocolSettingsJSON: []byte(`{"naive":{"port":443}}`),
	})
	if !strings.HasPrefix(uri, "naive+https://demo:secret@example.com:443") {
		t.Fatalf("unexpected naive uri: %s", uri)
	}
}

func TestBuildMieruURI(t *testing.T) {
	uri := BuildMieruURI(users.AccessWithNode{
		Access:                   users.Access{ProtocolUsername: "demo user", ProtocolPassword: "sec/ret"},
		NodeName:                 "Test Mieru Node",
		NodeDomain:               "chrono.vetka.tech",
		NodeProtocolSettingsJSON: []byte(`{"mieru":{"ports":["2012-2022","2030-2040"],"protocol":"TCP","mtu":1400,"multiplexing":"MULTIPLEXING_HIGH","handshake_mode":"HANDSHAKE_NO_WAIT","profile":"Test Mieru Node"}}`),
	}, false)
	if !strings.HasPrefix(uri, "mierus://") {
		t.Fatalf("expected mierus scheme, got %s", uri)
	}
	if strings.Contains(strings.ToLower(uri), "todo mieru") {
		t.Fatalf("placeholder remained in uri: %s", uri)
	}
	for _, want := range []string{
		"chrono.vetka.tech",
		"port=2012-2022",
		"port=2030-2040",
		"protocol=TCP",
		"mtu=1400",
		"multiplexing=MULTIPLEXING_HIGH",
		"handshake-mode=HANDSHAKE_NO_WAIT",
	} {
		if !strings.Contains(uri, want) {
			t.Fatalf("missing %q in %s", want, uri)
		}
	}
	if strings.Contains(uri, " ") {
		t.Fatalf("uri contains raw spaces: %s", uri)
	}
	if !strings.Contains(uri, "demo%20user") || !strings.Contains(uri, "sec%2Fret") {
		t.Fatalf("credentials were not escaped correctly: %s", uri)
	}
	if strings.Count(uri, "port=") != 2 {
		t.Fatalf("expected repeated port params, got %s", uri)
	}
	if strings.Count(uri, "protocol=") != 2 {
		t.Fatalf("expected protocol count to match port count, got %s", uri)
	}
}
