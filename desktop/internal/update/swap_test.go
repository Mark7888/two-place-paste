package update

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSwapInKeepsTheOldBuild(t *testing.T) {
	dir := t.TempDir()
	cur, next, prev := filepath.Join(dir, "app"), filepath.Join(dir, "app.new"), filepath.Join(dir, "app.old")
	write(t, cur, "v1")
	write(t, next, "v2")
	write(t, prev, "v0")

	if err := swapIn(cur, next, prev); err != nil {
		t.Fatal(err)
	}
	if got := read(t, cur); got != "v2" {
		t.Fatalf("current = %q, want v2", got)
	}
	if got := read(t, prev); got != "v1" {
		t.Fatalf("previous = %q, want v1 (v0 is replaced)", got)
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Fatalf("the staged build is still at %s", next)
	}
}

// A bundle is a directory; the same swap must work on one.
func TestSwapInWorksOnDirectories(t *testing.T) {
	dir := t.TempDir()
	cur, next, prev := filepath.Join(dir, "A.app"), filepath.Join(dir, "stage", "A.app"), filepath.Join(dir, ".A-previous.app")
	write(t, filepath.Join(cur, "Contents", "v"), "v1")
	write(t, filepath.Join(next, "Contents", "v"), "v2")

	if err := swapIn(cur, next, prev); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(cur, "Contents", "v")); got != "v2" {
		t.Fatalf("current = %q, want v2", got)
	}
	if got := read(t, filepath.Join(prev, "Contents", "v")); got != "v1" {
		t.Fatalf("previous = %q, want v1", got)
	}
}

func TestSwapInPutsTheRunningBuildBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	cur, prev := filepath.Join(dir, "app"), filepath.Join(dir, "app.old")
	write(t, cur, "v1")

	if err := swapIn(cur, filepath.Join(dir, "missing"), prev); err == nil {
		t.Fatal("swapIn with no staged build succeeded")
	}
	if got := read(t, cur); got != "v1" {
		t.Fatalf("current = %q after a failed swap, want v1", got)
	}
}

func TestSwapBackExchangesTheBuilds(t *testing.T) {
	dir := t.TempDir()
	cur, prev, scratch := filepath.Join(dir, "app"), filepath.Join(dir, "app.old"), filepath.Join(dir, "app.rollback")
	write(t, cur, "v2")
	write(t, prev, "v1")

	if err := swapBack(cur, prev, scratch); err != nil {
		t.Fatal(err)
	}
	if got := read(t, cur); got != "v1" {
		t.Fatalf("current = %q, want v1", got)
	}
	if got := read(t, prev); got != "v2" {
		t.Fatalf("previous = %q, want v2", got)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatal("the scratch name was left behind")
	}
}

func TestSwapBackWithoutAPreviousBuildChangesNothing(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "app")
	write(t, cur, "v2")
	if err := swapBack(cur, filepath.Join(dir, "app.old"), filepath.Join(dir, "app.rollback")); err == nil {
		t.Fatal("swapBack with nothing to restore succeeded")
	}
	if got := read(t, cur); got != "v2" {
		t.Fatalf("current = %q, want v2", got)
	}
}

func TestWritable(t *testing.T) {
	if err := writable(t.TempDir()); err != nil {
		t.Fatalf("a temp dir is not writable: %v", err)
	}
	if err := writable(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing directory is writable")
	}
}
