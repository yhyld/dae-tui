package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The save must leave a parseable file with 0600 permissions even when the
// previous file was looser: it stores the password and the bearer token.
func TestSaveIsAtomicAndEnforces0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	c := &Config{Endpoint: "http://127.0.0.1:2023/graphql", Username: "u", Password: "p", Token: "t1"}
	if err := c.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	// os.WriteFile would keep a pre-existing file's mode; the write-then-
	// rename replaces the file, so the mode is enforced again here.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	c.Token = "t2"
	if err := c.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Token != "t2" || got.Username != "u" {
		t.Fatalf("reload = %+v", got)
	}
	// No temp files left behind by the rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries after save, want 1", len(entries))
	}
}

// Driver hooks persist tokens/credentials from background goroutines while
// the UI may log out concurrently; the mutators serialize and every save is
// atomic, so the file always parses. Meaningful under -race.
func TestConcurrentMutationsAndSaves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	c := &Config{Endpoint: "http://127.0.0.1:2023/graphql", Username: "u", Password: "p"}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				_ = c.UpdateToken(path, "tok")
				_ = c.UpdateCredentials(path, "u", "p2")
				_ = c.Save(path)
			}
		}(i)
	}
	wg.Wait()
	if _, err := Load(path); err != nil {
		t.Fatalf("config corrupted by concurrent saves: %v", err)
	}
}
