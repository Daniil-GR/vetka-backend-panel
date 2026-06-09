package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaddyfileSubscriptionBlock(t *testing.T) {
	path := filepath.Join("..", "..", "Caddyfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read caddyfile: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"{$PANEL_DOMAIN}",
		"{$SUBSCRIPTION_DOMAIN}",
		"handle /sub/*",
		`respond "Not found" 404`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("caddyfile missing %q", want)
		}
	}
}
