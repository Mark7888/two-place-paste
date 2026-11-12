package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/admin/apiv1"
	"github.com/Mark7888/two-place-paste/server/internal/apikey"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// The admin REST API under /api/v1/ (docs/internal/plans/admin-rest-api.md).
// Its routes, request types and response types are generated from
// /api/openapi.yaml into package apiv1; this file is the front door every
// request passes first, and apiv1_handlers.go does the work through the
// same operations as the admin page.
//
// The admin session cookie is scoped to /admin and never reaches these
// routes, and nothing here reads a cookie: a key in the Authorization header
// is the only credential, so the API needs no CSRF defence. It answers no
// CORS preflight, so no browser page can call it with a visitor's key either.

const (
	apiBase = "/api/v1"

	// lastUsedEvery is how often a key's last use is written (D10).
	lastUsedEvery = time.Minute
)

type apiKeyCtxKey struct{}

// apiKeyFrom returns the API key a request authenticated with.
func apiKeyFrom(ctx context.Context) (store.APIKey, bool) {
	k, ok := ctx.Value(apiKeyCtxKey{}).(store.APIKey)
	return k, ok
}

// newAPIHandler builds the generated API on a mux of its own, which
// serveAPI fronts. Unknown paths get a problem, not net/http's plain text.
func (s *Server) newAPIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(apiBase+"/", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, codeNotFound, "There is no such endpoint. The reference is at /api/v1/openapi.yaml.")
	})
	strict := apiv1.NewStrictHandlerWithOptions(apiServer{s}, nil, apiv1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeProblem(w, http.StatusBadRequest, codeInvalid, "The request body is not valid JSON of the expected shape.")
		},
		ResponseErrorHandlerFunc: s.writeAPIError,
	})
	return apiv1.HandlerWithOptions(strict, apiv1.StdHTTPServerOptions{
		BaseURL:     apiBase,
		BaseRouter:  mux,
		Middlewares: []apiv1.MiddlewareFunc{s.requireScopes},
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			detail := "A parameter is not valid."
			var pe *apiv1.InvalidParamFormatError
			if errors.As(err, &pe) {
				detail = "The parameter " + pe.ParamName + " is not valid."
			}
			writeProblem(w, http.StatusBadRequest, codeInvalid, detail)
		},
	})
}

// writeAPIError answers a request whose handler returned an error: an
// *opError with its own status and code, anything else as internal.
func (s *Server) writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	oe := asOpError(err)
	if oe.status == http.StatusInternalServerError {
		var known *opError
		if !errors.As(err, &known) {
			s.logger.ErrorContext(r.Context(), "admin API request failed", slog.String("route", r.Pattern), "error", err)
		}
	}
	writeProblem(w, oe.status, oe.code, oe.msg)
}

// serveAPI is the front door of /api/v1/: the public description, or else
// authentication, the key's rate limit and a bounded body, in that order,
// before the generated router sees the request.
func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	ctx := r.Context()
	ip := clientIP(r)

	if spec, ctype, ok := publicSpec(r); ok {
		if ok, _ := s.specFetches.allow(ip); !ok {
			h.Set("Retry-After", "60")
			writeProblem(w, http.StatusTooManyRequests, codeRateLimited, "Too many requests. Wait a minute and try again.")
			return
		}
		h.Set("Content-Type", ctype)
		h.Set("Content-Length", strconv.Itoa(len(spec)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(spec)
		}
		return
	}

	if blocked, _ := s.apiFailures.blocked(ip); blocked {
		h.Set("Retry-After", "60")
		writeProblem(w, http.StatusTooManyRequests, codeRateLimited, "Too many failed attempts. Wait a minute and try again.")
		return
	}
	key, err := s.authenticate(ctx, r.Header.Get("Authorization"))
	if err != nil {
		if errors.Is(err, errBadKey) {
			s.refuseKey(w, r, ip)
		} else {
			s.logger.ErrorContext(ctx, "admin API: read API key", "error", err)
			writeProblem(w, http.StatusInternalServerError, codeInternal, "The API key could not be checked. Check the server log.")
		}
		return
	}

	limiter := s.apiWrites
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		limiter = s.apiReads
	}
	res := limiter.take(key.ID)
	h.Set("RateLimit-Limit", strconv.Itoa(res.limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(res.remaining))
	h.Set("RateLimit-Reset", strconv.Itoa(ceilSeconds(res.reset)))
	if !res.ok {
		h.Set("Retry-After", strconv.Itoa(max(1, ceilSeconds(res.retry))))
		writeProblem(w, http.StatusTooManyRequests, codeRateLimited, "This API key has used its request budget. Wait the number of seconds in Retry-After.")
		return
	}

	now := s.now()
	if now.Sub(key.LastUsedAt) >= lastUsedEvery {
		if err := s.apiKeys.TouchAPIKey(ctx, key.ID, now, ip); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.logger.WarnContext(ctx, "record API key use", "key_id", key.ID, "error", err)
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
	ctx = context.WithValue(ctx, apiKeyCtxKey{}, key)
	ctx = withActor(ctx, actor{kind: actorAPIKey, ip: ip, keyID: key.ID, keyName: key.Name})
	s.apiHandler.ServeHTTP(w, r.WithContext(ctx))
}

// publicSpec returns the API description a request asks for, if it asks for
// one of the two public routes.
func publicSpec(r *http.Request) ([]byte, string, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, "", false
	}
	switch r.URL.Path {
	case apiBase + "/openapi.yaml":
		return apiv1.SpecYAML, "application/yaml; charset=utf-8", true
	case apiBase + "/openapi.json":
		return apiv1.SpecJSON, "application/json; charset=utf-8", true
	}
	return nil, "", false
}

// errBadKey is a request without a working key: none, malformed, unknown,
// wrong, or expired. They are answered alike, so a probe learns nothing
// about which ids exist.
var errBadKey = errors.New("admin API: no valid API key")

// authenticate finds the key an Authorization header carries.
func (s *Server) authenticate(ctx context.Context, header string) (store.APIKey, error) {
	scheme, key, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return store.APIKey{}, errBadKey
	}
	key = strings.TrimSpace(key)
	id, ok := apikey.Parse(key)
	if !ok || s.apiKeys == nil {
		return store.APIKey{}, errBadKey
	}
	rec, err := s.apiKeys.GetAPIKey(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.APIKey{}, errBadKey
	}
	if err != nil {
		return store.APIKey{}, fmt.Errorf("authenticate API key %s: %w", id, err)
	}
	if !apikey.Matches(rec, key) || rec.Expired(s.now()) {
		return store.APIKey{}, errBadKey
	}
	return rec, nil
}

// refuseKey answers a request without a working key, and charges its address
// a failed attempt. The attempt that spends the last of the budget is logged
// and audited, like a sign-in lockout; serveAPI refuses the ones after it
// before they get here, so a lockout is one entry, not a flood.
func (s *Server) refuseKey(w http.ResponseWriter, r *http.Request, ip string) {
	ok, scope := s.apiFailures.allow(ip)
	if ok {
		_, scope = s.apiFailures.blocked(ip)
	}
	if scope != "" {
		s.logger.WarnContext(r.Context(), "admin API authentication rate limited",
			slog.String("client_ip", ip), slog.String("scope", scope))
		s.record(r.Context(), actor{kind: actorAPIKey, ip: ip}, "apikey.limited", "", scope)
	}
	if !ok {
		w.Header().Set("Retry-After", "60")
		writeProblem(w, http.StatusTooManyRequests, codeRateLimited, "Too many failed attempts. Wait a minute and try again.")
		return
	}
	// The route's pattern is not known yet and the path may hold a token
	// handle, so neither is logged; the key never is.
	s.logger.InfoContext(r.Context(), "admin API request refused: no valid API key", slog.String("client_ip", ip))
	w.Header().Set("WWW-Authenticate", `Bearer realm="tpp admin API"`)
	writeProblem(w, http.StatusUnauthorized, codeUnauthorized, "Send a valid API key: Authorization: Bearer tpp_ak_… The key may be wrong, expired or revoked.")
}

// requireScopes refuses a request whose key lacks a scope the operation
// requires. The generated router puts each operation's scopes on the
// context; an operation without any is public.
func (s *Server) requireScopes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		need, ok := r.Context().Value(apiv1.ApiKeyScopes).([]string)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		key, ok := apiKeyFrom(r.Context())
		if !ok {
			writeProblem(w, http.StatusUnauthorized, codeUnauthorized, "Send a valid API key.")
			return
		}
		for _, scope := range need {
			if !apikey.Allows(key.Scopes, scope) {
				writeProblem(w, http.StatusForbidden, codeForbidden, "This API key does not have the “"+scope+"” scope this call needs.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// problemJSON is an error as RFC 9457 has it, with the API's stable code.
type problemJSON struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	h := w.Header()
	h.Set("Content-Type", "application/problem+json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemJSON{
		Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code, Detail: detail,
	})
}

// ceilSeconds is d in whole seconds, rounded up.
func ceilSeconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}
