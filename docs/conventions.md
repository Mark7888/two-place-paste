# Engineering conventions

**Owned by:** Phase 0. Changes are their own PR (ROADMAP §3).
**Applies to:** all Go code in `/server`, `/pkg/tppclient`, `/desktop`. The
TypeScript sections apply to `/mobile` and `/desktop/ui`.

These are the rules a reviewer will hold a PR to. They exist so that four
phases written by four agents read like one codebase.

---

## 1. Errors

**Wrap with context, never with a stack trace.** Every error crossing a
function boundary gains one clause of context and preserves the cause:

```go
if err := store.ApplyRekey(ctx, groupID, wrapped); err != nil {
    return fmt.Errorf("apply rekey for group %s: %w", groupID, err)
}
```

- Always `%w`, never `%v`, when the caller might reasonably inspect the cause.
- The message is lowercase, no trailing punctuation, and describes the
  *operation*, not the error ("apply rekey", not "rekey failed").
- Do not repeat what the wrapped error already says. `open %s: %w` is right;
  `failed to open file %s because: %w` is not.
- Compare with `errors.Is` / `errors.As`. Never compare error strings.
- Sentinel errors are exported from the package that owns the concept:
  `store.ErrTokenConsumed`, `blob.ErrTooLarge`.
- Aggregate independent failures with `errors.Join` so an operator sees every
  problem at once — `config.Load` is the reference example.

**Never leak plaintext or secrets into an error.** Errors reach logs. An error
about an entry may name its ID, epoch and byte length; it may never contain
ciphertext, a key, a wrapped key, a token, or an admin password (SPEC §2.3).

## 2. Logging

`log/slog` only. No `fmt.Println`, no `log.Printf`, no third-party logger.

- Structured key/value attributes, never interpolated messages:
  `slog.Info("device paired", "group_id", gid, "device_id", did)`.
- Use the `Context` variants (`InfoContext`, `ErrorContext`) in request paths so
  future tracing has something to attach to.
- Levels: `Debug` for protocol frames and sweep detail, `Info` for lifecycle
  events (start, shutdown, group created, device revoked), `Warn` for recovered
  or rate-limited conditions, `Error` for anything an operator must act on.
- Libraries do not configure logging. `cmd/tpp/main.go` (P3e) builds the handler
  and calls `slog.SetDefault`; packages take a `*slog.Logger` or use the default.

**The log is part of the threat model.** SPEC §2.3 says the server does not see
clipboard content, content type, or filenames — Phase 7 audits the logs for
exactly this. Never log an entry body, a content type, a filename, a key, a
wrapped key, a creation token, a pairing token, or a session cookie. Log sizes,
IDs, epochs and timestamps freely.

## 3. Context

- Every function that does I/O takes `ctx context.Context` as its **first**
  parameter. No `context.Context` in a struct field.
- Propagate the incoming request context; never substitute `context.Background()`
  to "avoid cancellation".
- Background loops (the blob sweeper, the WebSocket hub) take a context and
  return when it is done. No `for {}` without a cancellation path.
- Only `main` and tests call `context.Background()` / `context.TODO()`.
- Timeouts belong at the I/O call site, not in the caller's head.

## 4. No panics in request paths

A malformed frame, an oversized upload, or a missing key is an error value, not
a panic. `httpapi` installs a recoverer as a **backstop**, not a licence: a
panic reaching it is a bug to fix.

Panic is acceptable only for programmer error detected at startup — an
impossible enum, a nil dependency that `main` must have wired.

## 5. Tests

**Table-driven by default:**

```go
func TestBucketForExpiry(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name string
        in   time.Time
        want string
    }{
        {name: "exact hour", in: ..., want: "2026090814"},
        {name: "rounds up",  in: ..., want: "2026090815"},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            ...
        })
    }
}
```

- `t.Parallel()` in every test and subtest that has no shared mutable state.
- Standard library only for assertions. No testify, no gomega. `t.Errorf` with
  a `got = %v, want %v` message says everything an assertion library would.
- Failure messages name the *thing*, not the line: `t.Errorf("Admin.UsesHash() =
  %v, want %v", got, want)`.
- `t.TempDir()` and `t.Cleanup` rather than manual teardown. Never write to a
  hardcoded path or to the real `/var`.
- Never call `time.Sleep` to wait for a condition. Use channels, `sync.WaitGroup`,
  or a polling helper with `t.Deadline`.
- Time-dependent code takes a clock (`func() time.Time`) or an explicit `now`
  argument. Blob bucket maths must be testable across a DST boundary in a
  non-UTC local zone (SPEC §4.5), which is impossible if it calls `time.Now`
  internally.
- Tests requiring Redis or another service are guarded and skipped with a
  reason when it is unavailable: `t.Skip("redis not available: ...")`.
- Run with `-race`. `make test` does.

## 6. Time

**All server-side time is UTC.** `time.Now().UTC()`. Local time and DST must
not be involved in blob bucket computation (SPEC §4.5) — a bucket boundary that
shifts with the host timezone silently deletes live blobs or leaks dead ones.

Durations in config are `time.Duration` parsed from strings like `10m`, never
bare integers whose unit lives in a comment.

## 7. Package layout

- `internal/` for everything not intended for another module. Only
  `pkg/tppclient` is a public API.
- One package, one responsibility. A package's doc comment states it in one
  sentence and cites the SPEC section it implements.
- Dependencies point inward: `ws` and `admin` depend on `store`, `entries` and
  `blob`; none of those depend on `ws` or `admin`.
- **Consume interfaces, define them at the consumer.** If `ws` needs three
  methods from the store, it declares a three-method interface locally. This is
  what lets P3a and P3c proceed without blocking each other.
- Exported identifiers have doc comments starting with the identifier name.

## 8. Concurrency

- Every goroutine has an owner that knows how it stops. No fire-and-forget.
- Bounded queues, never unbounded buffering. A slow WebSocket client is dropped,
  not accumulated (SPEC/ROADMAP P3c backpressure requirement).
- Guard shared state with a mutex or confine it to one goroutine. `-race` is
  mandatory in CI, so a data race is a build failure, not a flake.

## 9. Dependencies

The server is a self-hosted single binary; every dependency is a thing the
operator must trust and the maintainer must update.

- Prefer the standard library. `net/http`'s method-and-path patterns remove the
  need for a router dependency.
- A new module dependency is called out in the PR description with a reason.
- Crypto libraries are pinned by `/spec/crypto.md` (P2) and are not chosen
  per-phase.

## 10. Security-sensitive defaults

These are spec requirements, restated here because they are easy to lose in a
refactor:

- The desktop localhost server binds `127.0.0.1`, **never** `0.0.0.0`, and
  validates the `Origin` header on every request including WebSocket upgrades
  (SPEC §7.2).
- Admin session cookies are `HttpOnly`, `Secure`, `SameSite=Strict` (SPEC §4.4).
- `GET /<creation-token>` returns 404; only `POST` consumes a token (SPEC §3.1).
- Size limits are enforced at the reader with `io.LimitReader`, before
  buffering (SPEC §4.3).
- Comparisons of tokens, passwords and session IDs use
  `crypto/subtle.ConstantTimeCompare`.
- Randomness for tokens, nonces and keys comes from `crypto/rand`. `math/rand`
  never appears in a security path.

## 11. TypeScript

- `strict: true`. No `any` without a comment justifying it.
- Same error discipline: wrap with context, preserve the cause via `{ cause }`.
- Crypto is `@noble/*` per `/spec/crypto.md`, validated against
  `/spec/vectors` — the same JSON fixtures the Go client uses. A client that
  cannot reproduce the vectors does not ship.
- All platform access sits behind `src/platform/` with per-OS files, so iOS
  stays addable without restructuring (SPEC §7.3).

## 12. Commits and PRs

One phase, one PR (ROADMAP §0). A PR touches only paths its phase **Owns**. If
it needs a file outside that list, it stops and opens a `contract-change` issue
rather than editing — that single rule is what keeps parallel agents
conflict-free.
