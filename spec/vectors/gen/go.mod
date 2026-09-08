// Standalone module: the vector generator is a spec tool, not product code.
//
// It is deliberately NOT listed in the root go.work (that file is a shared
// touchpoint owned by Phase 0, ROADMAP §3). Run it with GOWORK=off:
//
//	GOWORK=off go run ./spec/vectors/gen -out spec/vectors
module github.com/Mark7888/two-place-paste/spec/vectors/gen

go 1.26.0

require golang.org/x/crypto v0.56.0

require golang.org/x/sys v0.47.0 // indirect
