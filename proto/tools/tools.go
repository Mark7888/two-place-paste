//go:build tools

// Package tools pins the protobuf code generators so that every developer and
// CI runner builds the same versions. It is never compiled into a binary; the
// `tools` build tag keeps it out of every ordinary build.
package tools

import (
	_ "github.com/bufbuild/buf/cmd/buf"
	_ "google.golang.org/protobuf/cmd/protoc-gen-go"
)
