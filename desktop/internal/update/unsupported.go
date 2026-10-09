//go:build !windows && !darwin

package update

import (
	"context"
	"errors"
)

// errNoApplier is what every operation of unsupportedApplier reports.
var errNoApplier = errors.New("updates cannot be installed on this platform")

// unsupportedApplier is the Applier for a platform with no published builds.
type unsupportedApplier struct{}

func (unsupportedApplier) Ready() error                          { return errNoApplier }
func (unsupportedApplier) Install(context.Context, string) error { return errNoApplier }
func (unsupportedApplier) HasPrevious() bool                     { return false }
func (unsupportedApplier) Rollback(context.Context) error        { return errNoApplier }
func (unsupportedApplier) Relaunch(int) error                    { return errNoApplier }

func platformApplier() Applier { return unsupportedApplier{} }
