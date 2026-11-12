//go:build tools

// Package tools pins the code generator for the admin API so that every
// developer and CI runner builds the same version. It is never compiled into a
// binary; the `tools` build tag keeps it out of every ordinary build.
package tools

import (
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
)
