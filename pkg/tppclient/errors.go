package tppclient

import (
	"errors"
	"fmt"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
)

// Sentinel conditions a caller branches on. Everything else is an ordinary
// wrapped error (docs/conventions.md §4).
var (
	// ErrNoGroup means this client has no group yet: call CreateGroup or
	// JoinPairing first.
	ErrNoGroup = errors.New("tppclient: this device is not in a group")

	// ErrNoEntry means the group has no unexpired entry. It is not a failure:
	// a newly paired device starts empty (SPEC §3.2).
	ErrNoEntry = errors.New("tppclient: the group has no entry")

	// ErrStaleEntry means the latest entry predates this client's epoch. Per
	// /spec/crypto.md §7 it is skipped silently — no error to the user, no
	// retry, no attempt to keep the old key. Such entries expire within 24
	// hours. A caller shows "nothing to sync", not a failure.
	ErrStaleEntry = errors.New("tppclient: entry is older than the current epoch")

	// ErrEpochAhead means the entry was written under a newer epoch than this
	// client holds: it is behind a rekey and its wrapped key has not arrived
	// yet. The key is pushed on connect, so the answer is to wait and retry
	// rather than to try other keys.
	ErrEpochAhead = errors.New("tppclient: entry is newer than the current epoch")

	// ErrClosed means Close has been called; the client cannot be reused.
	ErrClosed = errors.New("tppclient: client is closed")

	// ErrRevoked means the server no longer knows this device: it was revoked
	// while this client was away, and its credentials died with its record
	// (SPEC §3.3 step 5). Nothing recovers this install but pairing again.
	ErrRevoked = errors.New("tppclient: this device has been revoked")
)

// ProtocolError is an Error frame from the server. Callers branch on Code,
// never on Message (SPEC §5.1).
type ProtocolError struct {
	Code    tppv1.ErrorCode
	Message string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("tppclient: server error %s: %s", e.Code, e.Message)
}

// Is lets callers write errors.Is(err, ErrEpochConflict) for the codes that
// have a defined recovery.
func (e *ProtocolError) Is(target error) bool {
	code, ok := codeOf(target)
	return ok && code == e.Code
}

// codeError is a bare ErrorCode usable as a sentinel with errors.Is.
type codeError tppv1.ErrorCode

func (c codeError) Error() string {
	return fmt.Sprintf("tppclient: server error %s", tppv1.ErrorCode(c))
}

func codeOf(err error) (tppv1.ErrorCode, bool) {
	var c codeError
	if errors.As(err, &c) {
		return tppv1.ErrorCode(c), true
	}
	return 0, false
}

// Server-side conditions with a defined client response.
var (
	// ErrEpochConflict means another device rekeyed first, or an entry was
	// sealed under an epoch the group has moved past. Refetch the roster (or
	// wait for the wrapped key) and retry (SPEC §3.3 step 4).
	ErrEpochConflict error = codeError(tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT)

	// ErrTooLarge means the ciphertext exceeded the 10 MB per-entry cap
	// (SPEC §4.3).
	ErrTooLarge error = codeError(tppv1.ErrorCode_ERROR_CODE_TOO_LARGE)

	// ErrTokenConsumed means a creation or pairing token had already been
	// used. One token creates exactly one group (SPEC §3.1).
	ErrTokenConsumed error = codeError(tppv1.ErrorCode_ERROR_CODE_TOKEN_CONSUMED)

	// ErrTokenExpired means a pairing token passed its short lifetime
	// (SPEC §3.2).
	ErrTokenExpired error = codeError(tppv1.ErrorCode_ERROR_CODE_TOKEN_EXPIRED)

	// ErrNotFound means no such group, device, entry or pairing.
	ErrNotFound error = codeError(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND)

	// ErrUnauthenticated means the connection has no device identity, or the
	// identity was invalidated by a revocation.
	ErrUnauthenticated error = codeError(tppv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED)

	// ErrRateLimited means the caller should back off and retry.
	ErrRateLimited error = codeError(tppv1.ErrorCode_ERROR_CODE_RATE_LIMITED)
)
