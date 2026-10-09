package update

import (
	"errors"
	"fmt"
	"os"
)

// swapIn puts next in place of current and keeps current as previous,
// replacing whatever previous held. Both platforms swap this way: a running
// Windows .exe cannot be overwritten but can be renamed, and a macOS bundle
// must be replaced whole, never edited in place, or the running process can
// be killed for an invalid code signature.
//
// Every step is a rename within one directory, so it is atomic, and a failure
// half way puts current back.
func swapIn(current, next, previous string) error {
	if err := os.RemoveAll(previous); err != nil {
		return fmt.Errorf("remove the build kept from the update before last (%s): %w", previous, err)
	}
	if err := os.Rename(current, previous); err != nil {
		return fmt.Errorf("move the running build aside: %w", err)
	}
	if err := os.Rename(next, current); err != nil {
		if rerr := os.Rename(previous, current); rerr != nil {
			return errors.Join(
				fmt.Errorf("move the new build into place: %w", err),
				fmt.Errorf("and then put the old one back (it is at %s): %w", previous, rerr))
		}
		return fmt.Errorf("move the new build into place: %w", err)
	}
	return nil
}

// swapBack exchanges current and previous, so the build the last update
// replaced runs again and the one it replaced is kept in its place. scratch
// is a free name in the same directory.
func swapBack(current, previous, scratch string) error {
	if _, err := os.Stat(previous); err != nil {
		return fmt.Errorf("there is no previous build at %s: %w", previous, err)
	}
	if err := os.RemoveAll(scratch); err != nil {
		return fmt.Errorf("clear %s: %w", scratch, err)
	}
	if err := os.Rename(current, scratch); err != nil {
		return fmt.Errorf("move the running build aside: %w", err)
	}
	if err := os.Rename(previous, current); err != nil {
		if rerr := os.Rename(scratch, current); rerr != nil {
			return errors.Join(
				fmt.Errorf("move the previous build into place: %w", err),
				fmt.Errorf("and then put the running one back (it is at %s): %w", scratch, rerr))
		}
		return fmt.Errorf("move the previous build into place: %w", err)
	}
	if err := os.Rename(scratch, previous); err != nil {
		// The rollback itself succeeded; only the copy for going forward
		// again is lost.
		_ = os.RemoveAll(scratch)
	}
	return nil
}

// writable reports whether files can be created and renamed in dir.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".tpp-write-test-*")
	if err != nil {
		return fmt.Errorf("create a file in %s: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}
