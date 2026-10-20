package admin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// handleStyle serves the one stylesheet, and handleScript the one script.
// Both are public because the login page needs them before a session exists,
// and neither contains anything: the script reads the page it is on and makes
// no request of its own.
func (s *Server) handleStyle(w http.ResponseWriter, r *http.Request) {
	s.serveAsset(w, r, "style.css", "text/css; charset=utf-8")
}

func (s *Server) handleScript(w http.ResponseWriter, r *http.Request) {
	s.serveAsset(w, r, "app.js", "text/javascript; charset=utf-8")
}

// handleIcon serves one favicon. Public for the same reason as the stylesheet.
func (s *Server) handleIcon(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, r, name, contentType)
	}
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name, contentType string) {
	b, err := webadminFile(name)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "read admin asset", "asset", name, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// handleIndex renders the dashboard (SPEC §4.4).
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
	s.renderDashboard(w, r, sess)
}

// handleTokens renders the token screen (SPEC §4.4).
func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
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
	s.render(w, r, http.StatusOK, "login.html", pageData{Title: "Sign in", Centered: true})
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
		// never logged, here or anywhere (docs/internal/conventions.md §2).
		s.logger.WarnContext(r.Context(), "admin login rate limited",
			slog.String("client_ip", ip), slog.String("scope", scope))
		s.audit(r, "login.limited", "", scope)
		w.Header().Set("Retry-After", "60")
		s.render(w, r, http.StatusTooManyRequests, "login.html", pageData{
			Title:    "Sign in",
			Centered: true,
			Error:    "Too many sign-in attempts. Wait a minute and try again.",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
	if err := r.ParseForm(); err != nil {
		s.render(w, r, http.StatusBadRequest, "login.html", pageData{
			Title:    "Sign in",
			Centered: true,
			Error:    "That request could not be read.",
		})
		return
	}

	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("password")), s.password) != 1 {
		s.logger.WarnContext(r.Context(), "admin login failed", slog.String("client_ip", ip))
		s.audit(r, "login.failed", "", "")
		s.render(w, r, http.StatusUnauthorized, "login.html", pageData{
			Title:    "Sign in",
			Centered: true,
			Error:    "Incorrect password.",
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
	s.audit(r, "login", "", "")
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
		s.audit(r, "logout", "", "")
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
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
	if err := r.ParseForm(); err != nil {
		s.renderTokens(w, r, sess, http.StatusBadRequest, "That request could not be read.")
		return
	}
	if !s.checkCSRF(w, r, sess) {
		return
	}

	name, ok := validName(r.PostFormValue("name"), maxTokenNameLen)
	if !ok {
		s.renderTokens(w, r, sess, http.StatusBadRequest, "Give the token a name so you can tell it apart later.")
		return
	}
	note := strings.TrimSpace(r.PostFormValue("note"))
	if len(note) > maxNoteLen {
		s.renderTokens(w, r, sess, http.StatusBadRequest, fmt.Sprintf("Keep the note under %d characters.", maxNoteLen))
		return
	}
	tag := r.PostFormValue("tag")
	if tag != "" {
		if _, err := s.store.GetTag(r.Context(), tag); err != nil {
			s.renderTokens(w, r, sess, http.StatusBadRequest, "That tag no longer exists.")
			return
		}
	}
	var expires time.Time
	switch r.PostFormValue("expires") {
	case "", "never":
	case "1":
		expires = s.now().Add(24 * time.Hour)
	case "7":
		expires = s.now().Add(7 * 24 * time.Hour)
	case "30":
		expires = s.now().Add(30 * 24 * time.Hour)
	default:
		s.renderTokens(w, r, sess, http.StatusBadRequest, "Choose when the token expires from the list.")
		return
	}

	tok, err := s.store.CreateTokenWith(r.Context(), store.NewToken{Name: name, Note: note, Tag: tag, ExpiresAt: expires})
	if err != nil {
		s.logger.ErrorContext(r.Context(), "create token", "error", err)
		s.renderTokens(w, r, sess, http.StatusInternalServerError, "The token could not be created.")
		return
	}
	// The token value itself is a credential and is never logged
	// (docs/internal/conventions.md §2).
	s.logger.InfoContext(r.Context(), "creation token issued", slog.String("token_name", tok.Name))
	s.audit(r, "token.create", tok.Name, expiryText(tok.ExpiresAt))
	// Remember which token this redirect is carrying the user back to see, so
	// the listing opens its code straight away. It is held on the session for
	// one render rather than put in the query string: a creation URL in a
	// redirect target ends up in the browser's history and in every access log
	// between here and the operator.
	s.sessions.flash(sess.id, tok.Value)
	s.redirect(w, r, "/admin/tokens")
}

// handleRevokeToken deletes an unused creation token, so the URL that was
// handed out for it stops working. A used token is refused with a message
// rather than deleted: its group already exists, and removing the record
// would only hide which token created it.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
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

	value := r.PathValue("token")
	tok, err := s.store.GetToken(r.Context(), value)
	if err == nil {
		err = s.store.RevokeToken(r.Context(), value)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.renderTokens(w, r, sess, http.StatusNotFound, "That token no longer exists.")
		return
	case errors.Is(err, store.ErrTokenConsumed):
		s.renderTokens(w, r, sess, http.StatusConflict, "That token has already created a group, so there is nothing to revoke.")
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "revoke token", "error", err)
		s.renderTokens(w, r, sess, http.StatusInternalServerError, "The token could not be revoked.")
		return
	}
	// Logged by name only: the value is a credential (docs/internal/conventions.md §2).
	s.logger.InfoContext(r.Context(), "creation token revoked", slog.String("token_name", tok.Name))
	s.audit(r, "token.revoke", tok.Name, "")
	s.redirect(w, r, "/admin/tokens")
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
	if tok.Used || tok.Expired(s.now()) {
		// A consumed or expired token creates nothing; rendering it as a
		// scannable code would only invite someone to try.
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

// expiryText says when a token expires, for the audit log and the listing.
func expiryText(t time.Time) string {
	if t.IsZero() {
		return "never expires"
	}
	return "expires " + t.UTC().Format("2006-01-02 15:04 UTC")
}

// renderTokens renders the token screen for an authenticated session.
func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, sess session, status int, msg string) {
	data := s.page(sess, "Creation tokens", "tokens")
	data.Error = msg
	if tags, err := s.store.ListTags(r.Context()); err == nil {
		for _, t := range tags {
			data.TagNames = append(data.TagNames, t.Name)
		}
	}

	tokens, err := s.store.ListTokens(r.Context())
	if err != nil {
		s.logger.ErrorContext(r.Context(), "list tokens", "error", err)
		data.Error = "The token list could not be loaded."
		s.render(w, r, http.StatusInternalServerError, "tokens.html", data)
		return
	}
	// The flash is taken once, whether or not a matching token is still in
	// the listing: a code shown a second time on a reload is a code the user
	// did not ask to see again.
	fresh := s.sessions.takeFlash(sess.id)

	now := s.now()
	data.Tokens = make([]tokenView, 0, len(tokens))
	for i, t := range tokens {
		v := tokenView{
			Value:   t.Value,
			Name:    t.Name,
			Note:    t.Note,
			Tag:     t.Tag,
			Created: t.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"),
			Used:    t.Used,
			GroupID: t.GroupID,
			Index:   i,
		}
		switch {
		case t.Used && t.GroupDeleted:
			v.Status, v.StatusClass = "group deleted", "used"
		case t.Used:
			v.Status, v.StatusClass = "used", "used"
			v.GroupPath = groupPath(t.GroupID)
		case t.Expired(now):
			v.Status, v.StatusClass = "expired", "bad"
			v.Expired = true
			v.RevokePath = "/admin/tokens/" + url.PathEscape(t.Value) + "/revoke"
		default:
			v.Status, v.StatusClass = "unused", "open"
			v.URL = s.creationURL(t.Value)
			v.QRPath = "/admin/tokens/" + url.PathEscape(t.Value) + "/qr.png"
			v.RevokePath = "/admin/tokens/" + url.PathEscape(t.Value) + "/revoke"
			v.Fresh = fresh != "" && subtle.ConstantTimeCompare([]byte(fresh), []byte(t.Value)) == 1
		}
		if !t.Used && !t.ExpiresAt.IsZero() {
			if v.Expired {
				v.Expires = "expired " + ago(now, t.ExpiresAt, "")
			} else {
				v.Expires = "expires " + t.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
			}
		}
		data.Tokens = append(data.Tokens, v)
	}
	s.render(w, r, status, "tokens.html", data)
}

// page starts the data for an authenticated page, taking the session's
// pending confirmation with it.
func (s *Server) page(sess session, title, nav string) pageData {
	return pageData{
		Title:         title,
		Authenticated: true,
		CSRF:          sess.csrf,
		Nav:           nav,
		Notice:        s.sessions.takeNotice(sess.id),
	}
}

// formSession is the preamble of every admin form POST: a live session, a
// bounded body, and a matching CSRF token. On false a response has already
// been written.
func (s *Server) formSession(w http.ResponseWriter, r *http.Request) (session, bool) {
	sess, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/admin/login")
		return session{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return session{}, false
	}
	if !s.checkCSRF(w, r, sess) {
		return session{}, false
	}
	return sess, true
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
