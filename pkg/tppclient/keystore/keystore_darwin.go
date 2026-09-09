package keystore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// keychainTimeout bounds one `security` invocation. The CLI can block on a
// keychain prompt; a tray service that waited on one forever would look hung
// with nothing on screen to explain why.
const keychainTimeout = 15 * time.Second

// securityCmd builds one bounded `security` invocation.
func securityCmd(args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	return exec.CommandContext(ctx, "security", args...), cancel
}

// keychainNotFound is the exit status /usr/bin/security uses for "the item is
// not in the keychain".
const keychainNotFound = 44

// keychainStore keeps secrets as generic passwords in the user's login
// keychain, driven through /usr/bin/security.
//
// The CLI is used rather than the Security framework because the whole client
// is cgo-free: a cgo dependency here would have to be built and signed per
// architecture, for one API call. The cost is process spawning, which happens
// twice per launch.
type keychainStore struct {
	service string
}

func openOSStore(opts Options) (Store, error) {
	if _, err := exec.LookPath("security"); err != nil {
		return nil, fmt.Errorf("keystore: /usr/bin/security is missing: %w", ErrUnavailable)
	}
	return &keychainStore{service: opts.Service}, nil
}

func (k *keychainStore) Backend() Backend { return BackendKeychain }

func (k *keychainStore) Load(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	// -w prints the password and nothing else. It is on stdout, not in any
	// argument list, and this process is the only reader.
	cmd, cancel := securityCmd("find-generic-password", "-a", name, "-s", k.service, "-w")
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == keychainNotFound {
			return nil, fmt.Errorf("keystore: %q: %w", name, ErrNotFound)
		}
		return nil, fmt.Errorf("keystore: read %q from the keychain: %w: %s",
			name, err, strings.TrimSpace(stderr.String()))
	}
	secret, err := hex.DecodeString(strings.TrimSpace(stdout.String()))
	if err != nil {
		return nil, fmt.Errorf("keystore: %q is not in this package's format", name)
	}
	return secret, nil
}

func (k *keychainStore) Save(name string, secret []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	// The secret goes to the keychain hex-encoded, because a generic password
	// is a string and a key is arbitrary bytes.
	//
	// It is fed through `security -i`, which reads *commands* from stdin, so
	// that the secret never appears in this machine's process list — passing
	// it as -w's argument would expose it to every local `ps` for the life of
	// the call (docs/conventions.md §1).
	cmd, cancel := securityCmd("-i")
	defer cancel()
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -a %s -s %s -w %s\n",
		name, k.service, hex.EncodeToString(secret)))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keystore: write %q to the keychain: %w: %s",
			name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (k *keychainStore) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	cmd, cancel := securityCmd("delete-generic-password", "-a", name, "-s", k.service)
	defer cancel()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == keychainNotFound {
			return nil // already gone; the post-condition holds
		}
		return fmt.Errorf("keystore: delete %q from the keychain: %w: %s",
			name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
