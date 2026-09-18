package clipboard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUsableFile pins the guard that both platform reads put in front of
// os.ReadFile. Every false case here was once a read that failed and took the
// whole clipboard with it: a coerced text path ("/Group"), a copied folder,
// and a file that had been moved since it was copied.
func TestUsableFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "regular file", path: file, want: true},
		{name: "directory", path: dir, want: false},
		{name: "missing", path: filepath.Join(dir, "gone.txt"), want: false},
		{name: "empty", path: "", want: false},
		{name: "relative", path: "note.txt", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := usableFile(tt.path); got != tt.want {
				t.Errorf("usableFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
