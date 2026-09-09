package keystore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// openTestStore returns a file-backed store under a temp directory. The OS
// backend is skipped deliberately: a keychain test would prompt, and a test
// must never leave an item behind in a developer's login keychain.
func openTestStore(t *testing.T, passphrase string) Store {
	t.Helper()
	st, err := Open(Options{
		Service:    "TwoPlacePasteTest",
		Dir:        t.TempDir(),
		Passphrase: []byte(passphrase),
		ForceFile:  true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st
}

func TestFileStoreRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		passphrase string
		want       Backend
	}{
		{"encrypted", "correct horse battery staple", BackendEncryptedFile},
		{"plain", "", BackendFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openTestStore(t, tc.passphrase)
			if runtime.GOOS == "windows" {
				// Windows seals every file with DPAPI regardless of the
				// passphrase, which is the point of that backend.
				tc.want = BackendDPAPI
			}
			if got := st.Backend(); got != tc.want {
				t.Errorf("Backend() = %q, want %q", got, tc.want)
			}

			secret := []byte{0x00, 0x01, 0xff, 0x7f, 0x80}
			if err := st.Save("device-key", secret); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := st.Load("device-key")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !bytes.Equal(got, secret) {
				t.Errorf("Load = %x, want %x", got, secret)
			}

			// A rewrite replaces rather than appends.
			if err := st.Save("device-key", []byte("second")); err != nil {
				t.Fatalf("Save (replace): %v", err)
			}
			if got, err := st.Load("device-key"); err != nil || string(got) != "second" {
				t.Errorf("Load after replace = %q, %v; want \"second\", nil", got, err)
			}

			if err := st.Delete("device-key"); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := st.Load("device-key"); !errors.Is(err, ErrNotFound) {
				t.Errorf("Load after Delete = %v, want ErrNotFound", err)
			}
			// Deleting what is already gone is not an error: the
			// post-condition is the same.
			if err := st.Delete("device-key"); err != nil {
				t.Errorf("Delete of a missing secret = %v, want nil", err)
			}
		})
	}
}

func TestMissingSecretIsNotAnEmptyOne(t *testing.T) {
	st := openTestStore(t, "pass")
	got, err := st.Load("never-written")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load = %v, want ErrNotFound", err)
	}
	if got != nil {
		t.Errorf("Load returned %x with an error; want nil", got)
	}
}

// TestSecretFilesAreOwnerOnly pins the floor /spec/crypto.md §10 sets: a key at
// rest is readable by its owner and nobody else.
func TestSecretFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not the access control on Windows")
	}
	dir := t.TempDir()
	// A directory that already exists with loose permissions must be
	// tightened, not trusted.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	st, err := Open(Options{Service: "TwoPlacePasteTest", Dir: dir, Passphrase: []byte("p"), ForceFile: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Save("device-key", []byte("secret")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != secretDirMode {
		t.Errorf("directory mode = %v, want %v", got, secretDirMode)
	}
	fi, err := os.Stat(filepath.Join(dir, "device-key.secret"))
	if err != nil {
		t.Fatalf("stat secret: %v", err)
	}
	if got := fi.Mode().Perm(); got != secretFileMode {
		t.Errorf("secret mode = %v, want %v", got, secretFileMode)
	}
}

func TestEncryptedFileIsUnreadableWithoutThePassphrase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows backend is DPAPI, not a passphrase")
	}
	dir := t.TempDir()
	secret := []byte("device private key bytes")

	st, err := Open(Options{Dir: dir, Passphrase: []byte("right"), ForceFile: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Save("device-key", secret); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "device-key.secret"))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if bytes.Contains(raw, secret) {
		t.Error("the secret appears verbatim in the encrypted file")
	}

	wrong, err := Open(Options{Dir: dir, Passphrase: []byte("wrong"), ForceFile: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := wrong.Load("device-key"); err == nil {
		t.Error("Load with the wrong passphrase succeeded")
	}
}

// TestSaltIsFreshPerWrite proves two files under one passphrase do not share a
// key: identical plaintexts must not produce identical ciphertexts.
func TestSaltIsFreshPerWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows backend is DPAPI, not a passphrase")
	}
	dir := t.TempDir()
	st, err := Open(Options{Dir: dir, Passphrase: []byte("p"), ForceFile: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	same := []byte("identical plaintext")
	for _, name := range []string{"a", "b"} {
		if err := st.Save(name, same); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
	}
	a, err := os.ReadFile(filepath.Join(dir, "a.secret"))
	if err != nil {
		t.Fatalf("read a: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "b.secret"))
	if err != nil {
		t.Fatalf("read b: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Error("two writes of the same plaintext produced identical files")
	}
}

func TestRejectsUnusableNames(t *testing.T) {
	st := openTestStore(t, "p")
	for _, name := range []string{"", ".", "..", "../escape", "a/b", "with space", string(make([]byte, 65))} {
		if err := st.Save(name, []byte("x")); err == nil {
			t.Errorf("Save(%q) succeeded; a name must not reach the filesystem unchecked", name)
		}
		if _, err := st.Load(name); err == nil {
			t.Errorf("Load(%q) succeeded", name)
		}
	}
}

// TestSaveLeavesNoTemporaryFileBehind guards the atomic write: the directory
// must hold exactly the secrets, not the scratch files used to install them.
func TestSaveLeavesNoTemporaryFileBehind(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(Options{Dir: dir, Passphrase: []byte("p"), ForceFile: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Save("device-key", []byte("secret")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "device-key.secret" {
		t.Errorf("directory holds %v, want just the secret", names)
	}
}
