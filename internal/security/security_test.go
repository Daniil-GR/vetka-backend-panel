package security

import "testing"

func TestMaskSecret(t *testing.T) {
	got := MaskSecret("abcd12345678wxyz")
	if got != "abcd********wxyz" {
		t.Fatalf("unexpected mask: %s", got)
	}
	if got := MaskSecret("short"); got != "********" {
		t.Fatalf("short secret should be fully masked: %s", got)
	}
}
