package keystore

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/crypto/chacha20poly1305"
)

// secretFileMode is owner read/write and nothing else — the floor
// /spec/crypto.md §10 sets for a key at rest.
const secretFileMode fs.FileMode = 0o600

// secretDirMode is owner-only on the directory too, so the file names (which
// say what a client stores) are not world-readable either.
const secretDirMode fs.FileMode = 0o700

// fileKDFInfo is the domain-separation string for the passphrase-derived file
// key. It is distinct from every string in /spec/crypto.md §2.3: key storage
// is outside that profile and must not share its derivations.
const fileKDFInfo = "tpp/v1/keystore-file"

// saltSize is the per-file HKDF salt. A fresh salt per write means two files
// encrypted under one passphrase share no key.
const saltSize = 16

// protector seals and opens the bytes a fileStore writes. Each platform's
// strongest local protection plugs in here.
type protector interface {
	protect(plaintext []byte) ([]byte, error)
	open(sealed []byte) ([]byte, error)
	backend() Backend
}

// fileStore keeps one file per secret under a directory, sealed by a
// protector.
type fileStore struct {
	dir string
	p   protector
}

func openFileStore(opts Options) (Store, error) {
	dir := opts.Dir
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("keystore: locate the user configuration directory: %w", err)
		}
		dir = filepath.Join(base, opts.Service)
	}
	if err := os.MkdirAll(dir, secretDirMode); err != nil {
		return nil, fmt.Errorf("keystore: create %s: %w", dir, err)
	}
	// A directory that already existed may be too permissive; tighten it
	// rather than trusting whatever created it.
	if err := os.Chmod(dir, secretDirMode); err != nil {
		return nil, fmt.Errorf("keystore: tighten permissions on %s: %w", dir, err)
	}

	p, err := fileProtector(opts)
	if err != nil {
		return nil, err
	}
	return &fileStore{dir: dir, p: p}, nil
}

func (s *fileStore) Backend() Backend { return s.p.backend() }

func (s *fileStore) path(name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, name+".secret"), nil
}

func (s *fileStore) Load(name string) ([]byte, error) {
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}
	sealed, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("keystore: %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("keystore: read %q: %w", name, err)
	}
	secret, err := s.p.open(sealed)
	if err != nil {
		return nil, fmt.Errorf("keystore: open %q: %w", name, err)
	}
	return secret, nil
}

func (s *fileStore) Save(name string, secret []byte) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	sealed, err := s.p.protect(secret)
	if err != nil {
		return fmt.Errorf("keystore: seal %q: %w", name, err)
	}

	// Write through a temporary file in the same directory: a crash mid-write
	// must not leave a half-written key where a whole one used to be. The
	// temporary file carries the same mode, so the secret is never briefly
	// world-readable.
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("keystore: create a temporary file in %s: %w", s.dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(secretFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("keystore: set permissions on %q: %w", name, err)
	}
	if _, err := tmp.Write(sealed); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("keystore: write %q: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("keystore: flush %q: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("keystore: close %q: %w", name, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("keystore: install %q: %w", name, err)
	}
	return nil
}

func (s *fileStore) Delete(name string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("keystore: delete %q: %w", name, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Protectors
// ---------------------------------------------------------------------------

// plainProtector is the floor: no encryption, owner-only permissions. It is
// chosen only when the platform has no credential store and the caller gave no
// passphrase, and Backend() says so plainly so a UI can warn.
type plainProtector struct{}

func (plainProtector) protect(plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}

func (plainProtector) open(sealed []byte) ([]byte, error) {
	return append([]byte(nil), sealed...), nil
}

func (plainProtector) backend() Backend { return BackendFile }

// passphraseProtector encrypts with XChaCha20-Poly1305 under
// HKDF-SHA256(passphrase, salt, "tpp/v1/keystore-file").
//
// The KDF is deliberately the same one the crypto profile pins (spec §2.2), so
// the client needs no fourth primitive. Note what this is and is not: it binds
// the file to a passphrase the user supplies at unlock time, and it is not a
// password-hashing function — a low-entropy passphrase is brute-forceable
// offline by anyone holding the file. A passphrase-stretching KDF is the right
// answer where no OS store exists, and it is a crypto-contract change
// (/spec/crypto.md §12), not something to invent here.
type passphraseProtector struct {
	passphrase []byte
}

func (p passphraseProtector) backend() Backend { return BackendEncryptedFile }

func (p passphraseProtector) key(salt []byte) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, p.passphrase, salt, fileKDFInfo, chacha20poly1305.KeySize)
	if err != nil {
		return nil, fmt.Errorf("derive file key: %w", err)
	}
	return key, nil
}

func (p passphraseProtector) protect(plaintext []byte) ([]byte, error) {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	key, err := p.key(salt)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("initialise the file cipher: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	out := make([]byte, 0, 1+saltSize+aead.NonceSize()+len(plaintext)+aead.Overhead())
	out = append(out, fileFormatVersion)
	out = append(out, salt...)
	out = append(out, nonce...)
	// The version byte is bound in, as everything in this system binds its
	// version (/spec/crypto.md §2.4).
	return aead.Seal(out, nonce, plaintext, []byte{fileFormatVersion}), nil
}

func (p passphraseProtector) open(sealed []byte) ([]byte, error) {
	const header = 1 + saltSize + chacha20poly1305.NonceSizeX
	if len(sealed) < header+chacha20poly1305.Overhead {
		return nil, errors.New("the stored secret is truncated")
	}
	if sealed[0] != fileFormatVersion {
		return nil, fmt.Errorf("the stored secret has format version %d, this build writes %d", sealed[0], fileFormatVersion)
	}
	key, err := p.key(sealed[1 : 1+saltSize])
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("initialise the file cipher: %w", err)
	}
	plaintext, err := aead.Open(nil, sealed[1+saltSize:header], sealed[header:], []byte{fileFormatVersion})
	if err != nil {
		// A wrong passphrase and a corrupt file are one answer.
		return nil, errors.New("the stored secret could not be decrypted: wrong passphrase, or the file was altered")
	}
	return plaintext, nil
}

// fileFormatVersion prefixes every sealed file so that a future format is a
// rollout problem rather than a decryption failure.
const fileFormatVersion = 0x01

// defaultFileProtector is the portable choice: passphrase encryption where the
// caller supplied one, and permissions alone where it did not.
func defaultFileProtector(opts Options) protector {
	if len(opts.Passphrase) == 0 {
		return plainProtector{}
	}
	return passphraseProtector{passphrase: append([]byte(nil), opts.Passphrase...)}
}
