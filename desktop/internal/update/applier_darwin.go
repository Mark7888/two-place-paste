package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// bundleID is the CFBundleIdentifier every build carries
// (desktop/packaging/macos/Info.plist). A download that unpacks into anything
// else is not installed.
const bundleID = "com.twoplacepaste.desktop"

// darwinApplier replaces the whole .app bundle the service runs from: the
// bundle is never edited in place. The LaunchAgent points at the executable
// inside it, whose path does not change.
type darwinApplier struct {
	bundle string // …/TwoPlacePaste.app; empty if not running from a bundle
	err    error  // why bundle is empty
}

func platformApplier() Applier {
	bundle, err := runningBundle()
	return &darwinApplier{bundle: bundle, err: err}
}

// runningBundle finds the .app this process runs from:
// <bundle>/Contents/MacOS/tppdesktop.
func runningBundle() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("the running program could not be located: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", fmt.Errorf("the running program could not be located: %w", err)
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if filepath.Ext(bundle) != ".app" || filepath.Base(filepath.Dir(exe)) != "MacOS" {
		return "", errors.New("TwoPlacePaste is not running from its .app bundle, so it cannot replace itself")
	}
	return bundle, nil
}

// previous is where the replaced bundle is kept, hidden next to the live one.
func (a *darwinApplier) previous() string {
	name := strings.TrimSuffix(filepath.Base(a.bundle), ".app")
	return filepath.Join(filepath.Dir(a.bundle), "."+name+"-previous.app")
}

func (a *darwinApplier) Ready() error {
	if a.err != nil {
		return a.err
	}
	// Gatekeeper runs a quarantined app from a randomised read-only copy
	// until it has been moved; nothing written there would last.
	if strings.Contains(a.bundle, "/AppTranslocation/") {
		return errors.New("move TwoPlacePaste to the Applications folder before updating it")
	}
	if err := writable(filepath.Dir(a.bundle)); err != nil {
		return fmt.Errorf("TwoPlacePaste is in %s, which this account cannot write to; "+
			"move it to ~/Applications to update it without an administrator password", filepath.Dir(a.bundle))
	}
	return nil
}

// Install unpacks the verified zip next to the running bundle, checks what it
// unpacked is this app and intact, and swaps it in. The zip was downloaded by
// this process, which sets no quarantine flag, so the new build opens without
// a trip to Privacy & Security.
func (a *darwinApplier) Install(ctx context.Context, path string) error {
	if err := a.Ready(); err != nil {
		return err
	}
	parent := filepath.Dir(a.bundle)
	stage, err := os.MkdirTemp(parent, ".TwoPlacePaste-update-")
	if err != nil {
		return fmt.Errorf("create a staging folder in %s: %w", parent, err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	if err := run(ctx, "ditto", "-x", "-k", path, stage); err != nil {
		return fmt.Errorf("unpack the update: %w", err)
	}
	next, err := singleApp(stage)
	if err != nil {
		return err
	}
	id, err := output(ctx, "plutil", "-extract", "CFBundleIdentifier", "raw", "-o", "-",
		filepath.Join(next, "Contents", "Info.plist"))
	if err != nil {
		return fmt.Errorf("read the update's bundle identifier: %w", err)
	}
	if id != bundleID {
		return fmt.Errorf("the update is %q, not %s", id, bundleID)
	}
	// The builds are ad-hoc signed; this proves the bundle is the one CI
	// signed, intact, rather than that Apple knows its signer.
	if err := run(ctx, "codesign", "--verify", "--deep", "--strict", next); err != nil {
		return fmt.Errorf("the update's code signature does not verify: %w", err)
	}
	if err := swapIn(a.bundle, next, a.previous()); err != nil {
		return err
	}
	_ = run(ctx, "xattr", "-dr", "com.apple.quarantine", a.bundle)
	return nil
}

func (a *darwinApplier) HasPrevious() bool {
	if a.err != nil {
		return false
	}
	_, err := os.Stat(a.previous())
	return err == nil
}

func (a *darwinApplier) Rollback(context.Context) error {
	if err := a.Ready(); err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(a.bundle), ".app")
	return swapBack(a.bundle, a.previous(), filepath.Join(filepath.Dir(a.bundle), "."+name+"-rollback.app"))
}

// Relaunch opens a new instance of the bundle (-n: this one is still
// running) and tells it to wait for this one to exit.
func (a *darwinApplier) Relaunch(pid int) error {
	cmd := exec.CommandContext(context.Background(), "open", "-n", a.bundle, "--args", waitPIDFlag, strconv.Itoa(pid))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open %s: %w: %s", a.bundle, err, bytes.TrimSpace(out))
	}
	return nil
}

// singleApp is the one .app a build's zip unpacks to.
func singleApp(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read the unpacked update: %w", err)
	}
	var apps []string
	for _, e := range entries {
		if e.IsDir() && filepath.Ext(e.Name()) == ".app" {
			apps = append(apps, filepath.Join(dir, e.Name()))
		}
	}
	if len(apps) != 1 {
		return "", fmt.Errorf("the update holds %d app bundles, not one", len(apps))
	}
	return apps[0], nil
}

func run(ctx context.Context, name string, args ...string) error {
	_, err := output(ctx, name, args...)
	return err
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return strings.TrimSpace(string(out)), nil
}
