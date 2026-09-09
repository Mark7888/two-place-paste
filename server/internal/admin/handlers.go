package admin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"rsc.io/qr"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// handleStyle serves the one stylesheet. It is public because the login page
// needs it before a session exists, and it contains nothing.
func (s *Server) handleStyle(w http.ResponseWriter, r *http.Request) {
	b, err := webadminFile("style.css")
	if err != nil {
		s.logger.ErrorContext(r.Context(), "read admin stylesheet", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// handleIndex renders the token screen (SPEC §4.4).
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// "GET /admin/" also matches every unregistered path below /admin/.
	if r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}
	sess, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/admin/login")
		return
	}
	s.renderTokens(w, r, sess, http.StatusOK, "")
}

// handleLoginForm renders the login page.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); ok {
		s.redirect(w, r, "/admin/")
		return
	}
	s.render(w, r, http.StatusOK, "login.html", pageData{Title: "Sign in"})
}

// handleLogin verifies the admin password and starts a session.
//
// This endpoint is publicly reachable, so it is rate limited before the
// password is looked at: a refusal must cost an attacker an attempt, not a
// comparison.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, scope := s.logins.allow(ip); !ok {
		// Logged at Warn: an operator wants to see lockouts. The password is
		// never logged, here or anywhere (docs/conventions.md §2).
		s.logger.WarnContext(r.Context(), "admin login rate limited",
			slog.String("client_ip", ip), slog.String("scope", scope))
		w.Header().Set("Retry-After", "60")
		s.render(w, r, http.StatusTooManyRequests, "login.html", pageData{
			Title: "Sign in",
			Error: "Too many sign-in attempts. Wait a minute and try again.",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
	if err := r.ParseForm(); err != nil {
		s.render(w, r, http.StatusBadRequest, "login.html", pageData{
			Title: "Sign in",
			Error: "That request could not be read.",
		})
		return
	}

	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("password")), s.password) != 1 {
		s.logger.WarnContext(r.Context(), "admin login failed", slog.String("client_ip", ip))
		s.render(w, r, http.StatusUnauthorized, "login.html", pageData{
			Title: "Sign in",
			Error: "Incorrect password.",
		})
		return
	}

	sess := s.sessions.create()
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookieName,
		Value: sess.id,
		Path:  sessionCookiePath,
		// SPEC §4.4: HttpOnly and SameSite=Strict, and Secure whenever the
		// deployment is reachable over https (see Server.secureCookies).
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteStrictMode,
		Expires:  sess.expiresAt,
		MaxAge:   int(s.sessions.ttl.Seconds()),
	})
	s.logger.InfoContext(r.Context(), "admin signed in", slog.String("client_ip", ip))
	s.redirect(w, r, "/admin/")
}

// handleLogout destroys the session server-side and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(r)
	if ok {
		if !s.checkCSRF(w, r, sess) {
			return
		}
		s.sessions.destroy(sess.id)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     sessionCookiePath,
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	s.redirect(w, r, "/admin/login")
}

// handleCreateToken mints a creation token (SPEC §3.1 step 1).
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/admin/login")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
	if err := r.ParseForm(); err != nil {
		s.renderTokens(w, r, sess, http.StatusBadRequest, "That request could not be read.")
		return
	}
	if !s.checkCSRF(w, r, sess) {
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		s.renderTokens(w, r, sess, http.StatusBadRequest, "Give the token a name so you can tell it apart later.")
		return
	}
	if len(name) > maxTokenNameLen {
		name = name[:maxTokenNameLen]
	}

	tok, err := s.store.CreateToken(r.Context(), name)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "create token", "error", err)
		s.renderTokens(w, r, sess, http.StatusInternalServerError, "The token could not be created.")
		return
	}
	// The token value itself is a credential and is never logged
	// (docs/conventions.md §2).
	s.logger.InfoContext(r.Context(), "creation token issued", slog.String("token_name", tok.Name))
	s.redirect(w, r, "/admin/")
}

// handleTokenQR renders a token's creation URL as a QR code, so first-device
// onboarding is a scan rather than typing a URL on a phone (SPEC §4.4).
func (s *Server) handleTokenQR(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	value := r.PathValue("token")
	tok, err := s.store.GetToken(r.Context(), value)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.logger.ErrorContext(r.Context(), "read token", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if tok.Used {
		// A consumed token creates nothing; rendering it as a scannable code
		// would only invite someone to try.
		http.NotFound(w, r)
		return
	}

	code, err := qr.Encode(s.creationURL(tok.Value), qr.M)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "encode token qr", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	// The image encodes a live credential: no cache, no proxy copy.
	w.Header().Set("Cache-Control", "no-store, private")
	_, _ = w.Write(code.PNG())
}

// createGroupRequest is the body of POST /<token> (SPEC §3.1 step 4). Keys are
// base64 so the request is ordinary JSON; the server treats both as opaque
// bytes whose layout is pinned by /spec/crypto.md.
type createGroupRequest struct {
	DeviceName      string `json:"device_name"`
	DevicePublicKey []byte `json:"device_public_key"`
	WrappedGroupKey []byte `json:"wrapped_group_key"`
}

// createGroupResponse is what a successful creation returns.
type createGroupResponse struct {
	GroupID  string `json:"group_id"`
	DeviceID string `json:"device_id"`
	Epoch    uint64 `json:"epoch"`
}

// handleTokenGet answers every GET of a creation URL with 404 (SPEC §3.1).
//
// The token is a credential that a single request consumes, so anything that
// follows links — a crawler, a chat client's link preview, an email scanner —
// would otherwise burn it before the user ever opened it. Answering 404 makes
// that impossible rather than unlikely: there is no GET handler to reach.
func (s *Server) handleTokenGet(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

// handleTokenPost consumes a creation token and creates the group and its
// first device in one atomic store operation (SPEC §3.1 steps 4-5).
func (s *Server) handleTokenPost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCreateBodyBytes))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "request body could not be read")
		return
	}
	var req createGroupRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}
	if len(req.DevicePublicKey) == 0 || len(req.WrappedGroupKey) == 0 {
		writeJSONError(w, http.StatusBadRequest, "device_public_key and wrapped_group_key are required")
		return
	}

	created, err := s.store.CreateGroup(r.Context(), r.PathValue("token"), store.NewDevice{
		Name:            req.DeviceName,
		PublicKey:       req.DevicePublicKey,
		WrappedGroupKey: req.WrappedGroupKey,
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Deliberately indistinguishable from any other unknown path: a
		// probe learns nothing about which tokens exist.
		http.NotFound(w, r)
		return
	case errors.Is(err, store.ErrTokenConsumed):
		writeJSONError(w, http.StatusConflict, "this token has already created a group")
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "create group from token", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	s.logger.InfoContext(r.Context(), "group created",
		slog.String("group_id", created.Group.ID),
		slog.String("device_id", created.Device.ID))

	writeJSON(w, http.StatusCreated, createGroupResponse{
		GroupID:  created.Group.ID,
		DeviceID: created.Device.ID,
		Epoch:    created.Group.Epoch,
	})
}

// renderTokens renders the token screen for an authenticated session.
func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, sess session, status int, msg string) {
	data := pageData{Title: "Creation tokens", Authenticated: true, CSRF: sess.csrf, Error: msg}

	tokens, err := s.store.ListTokens(r.Context())
	if err != nil {
		s.logger.ErrorContext(r.Context(), "list tokens", "error", err)
		data.Error = "The token list could not be loaded."
		s.render(w, r, http.StatusInternalServerError, "tokens.html", data)
		return
	}
	data.Tokens = make([]tokenView, 0, len(tokens))
	for _, t := range tokens {
		v := tokenView{
			Value:   t.Value,
			Name:    t.Name,
			Created: t.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"),
			Used:    t.Used,
			GroupID: t.GroupID,
		}
		if !t.Used {
			v.URL = s.creationURL(t.Value)
		}
		data.Tokens = append(data.Tokens, v)
	}
	s.render(w, r, status, "tokens.html", data)
}

// session returns the request's session, if it has a live one.
func (s *Server) session(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return session{}, false
	}
	return s.sessions.get(c.Value)
}

// checkCSRF verifies the form token. SameSite=Strict already keeps a
// cross-site form from carrying the cookie; this is the second lock, because
// the first one is a browser policy rather than something the server enforces.
func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request, sess session) bool {
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.csrf)) == 1 {
		return true
	}
	s.logger.WarnContext(r.Context(), "admin request rejected: bad csrf token",
		slog.String("client_ip", clientIP(r)), slog.String("path", r.URL.Path))
	http.Error(w, "bad request", http.StatusBadRequest)
	return false
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// clientIP is the address the rate limiter charges.
//
// SPEC §4.1 puts a reverse proxy in front of the server, so RemoteAddr is the
// proxy for every request and per-IP limiting would collapse into one bucket.
// X-Forwarded-For is therefore honoured, but only when the immediate peer is
// loopback or a private address — i.e. plausibly that proxy — and only its
// rightmost entry, which is the one the last proxy wrote. Entries to its left
// are attacker-controlled and are ignored.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || (!peer.IsLoopback() && !peer.IsPrivate() && !peer.IsLinkLocalUnicast()) {
		return host
	}
	fwd := r.Header.Get("X-Forwarded-For")
	if fwd == "" {
		return host
	}
	parts := strings.Split(fwd, ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if ip := net.ParseIP(last); ip != nil {
		return ip.String()
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
