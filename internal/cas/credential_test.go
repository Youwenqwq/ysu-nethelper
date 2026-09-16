package cas

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialRoundTripKeepsUsername(t *testing.T) {
	c := New(time.Second)
	c.username = "202411040369"
	path := filepath.Join(t.TempDir(), "cas.json")
	if err := c.SaveCredential(path); err != nil {
		t.Fatal(err)
	}

	c2 := New(time.Second)
	if err := c2.LoadCredential(path); err != nil {
		t.Fatal(err)
	}
	if got := c2.Username(); got != "202411040369" {
		t.Fatalf("Username() = %q, want %q", got, "202411040369")
	}
}

func TestLoadLegacyCredentialWithoutUsername(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cas.json")
	if err := os.WriteFile(path, []byte(`{"cookies":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New(time.Second)
	if err := c.LoadCredential(path); err != nil {
		t.Fatal(err)
	}
	if got := c.Username(); got != "" {
		t.Fatalf("Username() = %q, want empty for legacy file", got)
	}
}
