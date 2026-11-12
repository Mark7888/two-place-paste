package admin

import (
	"context"
	"errors"
	"net/http"
)

// The admin page and the REST API (docs/internal/plans/admin-rest-api.md
// D26) do the same things, so they share one implementation: methods that
// take a context and return a result or an *opError. The page's handlers
// turn an *opError into the sentence they show, the API's into a problem
// with a stable code. Neither surface may check or change anything the other
// does not.

// opError is an admin operation that was refused or failed: the HTTP status,
// the REST API's stable code, and the sentence the admin page shows (which
// the API sends as its detail).
type opError struct {
	status int
	code   string
	msg    string
}

func (e *opError) Error() string { return e.msg }

// The REST API's problem codes (api/openapi.yaml, Problem.code).
const (
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeNotFound     = "not_found"
	codeInvalid      = "invalid_request"
	codeConflict     = "conflict"
	codeRateLimited  = "rate_limited"
	codeUnavailable  = "unavailable"
	codeInternal     = "internal"
)

func invalid(msg string) error     { return &opError{http.StatusBadRequest, codeInvalid, msg} }
func notFound(msg string) error    { return &opError{http.StatusNotFound, codeNotFound, msg} }
func conflict(msg string) error    { return &opError{http.StatusConflict, codeConflict, msg} }
func unavailable(msg string) error { return &opError{http.StatusNotImplemented, codeUnavailable, msg} }
func failed(msg string) error      { return &opError{http.StatusInternalServerError, codeInternal, msg} }

// asOpError is err as an *opError. Anything else is an internal error whose
// text is not shown: it may name a Redis key or an id.
func asOpError(err error) *opError {
	var oe *opError
	if errors.As(err, &oe) {
		return oe
	}
	return &opError{http.StatusInternalServerError, codeInternal, "Something went wrong. Check the server log."}
}

// writeOpError answers the admin page with an operation's error.
func writeOpError(w http.ResponseWriter, err error) {
	oe := asOpError(err)
	writeJSONError(w, oe.status, oe.msg)
}

// actor is who is making a request, for the audit log: a signed-in admin
// page, an API key, or the tpp command line.
type actor struct {
	// kind is "" for the admin page, actorAPIKey or actorCLI, as
	// store.AuditEntry.Actor stores it.
	kind string

	// ip is the client address; empty for the command line.
	ip string

	// keyID and keyName name the API key.
	keyID   string
	keyName string
}

// The actor kinds besides the admin page.
const (
	actorAPIKey = "api_key"
	actorCLI    = "cli"
)

type actorCtxKey struct{}

// withActor returns ctx carrying a.
func withActor(ctx context.Context, a actor) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, a)
}

// actorFrom returns the actor ctx carries.
func actorFrom(ctx context.Context) (actor, bool) {
	a, ok := ctx.Value(actorCtxKey{}).(actor)
	return a, ok
}
