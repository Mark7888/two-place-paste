package update

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// windowsApplier replaces tppdesktop.exe where it is installed, normally the
// per-user %LOCALAPPDATA%\Programs\TwoPlacePaste the installer chose. The
// path never changes, so the HKCU Run login item stays valid.
type windowsApplier struct {
	exe string // the running executable; empty if it could not be found
	err error  // why exe could not be found
}

func platformApplier() Applier {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	return &windowsApplier{exe: exe, err: err}
}

func (a *windowsApplier) previous() string { return a.exe + ".old" }

func (a *windowsApplier) Ready() error {
	if a.err != nil {
		return fmt.Errorf("the running program could not be located: %w", a.err)
	}
	if err := writable(filepath.Dir(a.exe)); err != nil {
		return fmt.Errorf("TwoPlacePaste is installed in %s, which this account cannot write to", filepath.Dir(a.exe))
	}
	return nil
}

// Install copies the verified download next to the running .exe, then swaps
// the two by renaming: a running .exe cannot be overwritten, but it can be
// renamed. The download was made by this process, so it carries no
// Mark-of-the-Web and SmartScreen never asks about it.
func (a *windowsApplier) Install(_ context.Context, path string) error {
	if err := a.Ready(); err != nil {
		return err
	}
	next := a.exe + ".new"
	if err := copyFile(path, next); err != nil {
		return err
	}
	if err := swapIn(a.exe, next, a.previous()); err != nil {
		_ = os.Remove(next)
		return err
	}
	return nil
}

func (a *windowsApplier) HasPrevious() bool {
	if a.err != nil {
		return false
	}
	_, err := os.Stat(a.previous())
	return err == nil
}

func (a *windowsApplier) Rollback(context.Context) error {
	if err := a.Ready(); err != nil {
		return err
	}
	return swapBack(a.exe, a.previous(), a.exe+".rollback")
}

// Relaunch starts the installed .exe detached from this process, so it
// outlives it, and tells it to wait for this one to exit.
func (a *windowsApplier) Relaunch(pid int) error {
	// Background: the new process must outlive this one, so nothing may
	// cancel it.
	cmd := exec.CommandContext(context.Background(), a.exe, waitPIDFlag, strconv.Itoa(pid))
	cmd.Dir = filepath.Dir(a.exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", a.exe, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release %s: %w", a.exe, err)
	}
	return nil
}

// copyFile copies src to a new dst, flushed to disk before it is renamed into
// place.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open the download: %w", err)
	}
	defer func() { _ = in.Close() }()
	_ = os.Remove(dst)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer func() {
		if cerr := out.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("close %s: %w", dst, cerr)
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("flush %s: %w", dst, err)
	}
	return nil
}
