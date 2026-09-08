// Package tppclient is the shared client core for TwoPlacePaste: crypto,
// WebSocket protocol client, and the high-level flows (create group, pair,
// revoke, put/get entries).
//
// Deliberate invariant (SPEC §3.3, ROADMAP P4): this package has no access to
// the OS clipboard. "The local clipboard is never touched by a rekey" is
// therefore structurally guaranteed rather than merely tested.
//
// Implementation lands in Phase 4; Phase 0 provides the module skeleton only.
package tppclient
