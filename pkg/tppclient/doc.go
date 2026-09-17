// Package tppclient is the shared client core for TwoPlacePaste: the crypto of
// /spec/crypto.md, a WebSocket protocol client, and one method per flow of
// SPEC §3 and §6 — CreateGroup, StartPairing, JoinPairing, StartOffer,
// PrepareAcceptOffer, Devices, Revoke, PutEntry, GetLatest, GetHistory,
// GetEntry.
//
// The desktop app, and any future CLI or Linux client, are shells around this
// package: it holds all of the crypto and all of the protocol, and they hold
// none.
//
// # Layout
//
//	tppcrypto   the crypto profile, byte for byte, validated against
//	            /spec/vectors — the only place a key is derived
//	keystore    where the device private key and the group key live at rest
//	(this)      transport, epoch handling and the flows
//
// # Pairing runs in both directions
//
// StartPairing and JoinPairing are member-emitted: the device that holds a
// group key shows a code, and the device without one consumes it. StartOffer
// and PrepareAcceptOffer are the same exchange with the roles swapped, so that
// whichever device has the screen the user is looking at can be the one
// showing something.
//
// The risk is not symmetric, and neither is the API. A hostile invitation
// costs a joiner nothing: it holds no key to lose. A hostile offer is accepted
// by a member, who hands over the group key — so accepting is split into
// PrepareAcceptOffer, which changes nothing and returns the name and
// fingerprint a dialog must render, and Confirm, which is the only thing that
// wraps a key. That is the shape of Revoke and Revocation.Confirm, for the
// same reason: consent is not something a screen can be trusted to
// remember.
//
// # Deliberate invariant
//
// This package has no access to the OS clipboard, and must never gain one.
// That is what structurally guarantees SPEC §3.3's rule that a rekey never
// touches the local clipboard: installing a new epoch is a key operation, and
// there is nothing here that could make it anything else. A shell reads and
// writes the clipboard; the library never learns that a clipboard exists.
//
// # Epochs
//
// A client holds exactly one (epoch, group key) pair (/spec/crypto.md §7).
// Entries below that epoch are skipped silently — never decrypted with a
// retained old key, because forward secrecy across revocation is the whole
// point of the rekey — and a wrapped key for a newer epoch, whether pushed
// during a rekey or waiting at connect time, advances it.
//
// # Concurrency
//
// A Client is safe for concurrent use. One connection is supervised in the
// background and re-dialled with jittered exponential backoff; requests wait
// for it rather than failing while it is down.
package tppclient
