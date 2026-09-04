package fsmgr

import (
	"os"
	"path/filepath"
	"testing"
)

// A pre-planted symlink must not allow escape from the instance root.
func TestSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Read("link/secret.txt"); err == nil {
		t.Fatal("Read through symlink should be rejected")
	}
	if err := m.Delete("link/secret.txt"); err == nil {
		t.Fatal("Delete through symlink should be rejected")
	}
	if _, err := m.List("link"); err == nil {
		t.Fatal("List through symlink should be rejected")
	}
	if _, err := m.SafeJoin("../outside"); err == nil {
		t.Fatal("lexical traversal should be rejected")
	}
}

// Regular files inside the root keep working.
func TestNormalReadWrite(t *testing.T) {
	root := t.TempDir()
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	re := NewRegistry()
	re.Bind(1, root)
	mm, err := re.For(1, root)
	if err != nil {
		t.Fatal(err)
	}
	_ = m
	if err := mm.Write("sub/file.txt", "aGVsbG8="); err != nil { // "hello"
		t.Fatal(err)
	}
	res, err := mm.Read("sub/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentB64 != "aGVsbG8=" {
		t.Fatalf("unexpected content %q", res.ContentB64)
	}
	// A mismatched network-supplied Dir must not move the root.
	if _, err := re.For(1, t.TempDir()); err == nil {
		t.Fatal("root mismatch should be rejected")
	}
}
