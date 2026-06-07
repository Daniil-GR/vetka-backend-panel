package subscriptions

import (
	"strings"
	"testing"

	"vetka-backend-panel/internal/users"
)

func TestBuildNaiveURI(t *testing.T) {
	uri := BuildNaiveURI(users.AccessWithNode{
		Access:   users.Access{ProtocolUsername: "demo", ProtocolPassword: "secret"},
		NodeName: "Node One", NodeDomain: "example.com",
	})
	if !strings.HasPrefix(uri, "naive+https://demo:secret@example.com:443") {
		t.Fatalf("unexpected naive uri: %s", uri)
	}
}

func TestBuildMieruURIDevMode(t *testing.T) {
	uri := BuildMieruURI(users.AccessWithNode{
		Access:   users.Access{ProtocolUsername: "demo", ProtocolPassword: "secret"},
		NodeName: "Mieru", NodeDomain: "example.com",
	}, true)
	if !strings.HasPrefix(uri, "# TODO mieru share format:") {
		t.Fatalf("expected dev placeholder, got %s", uri)
	}
}
