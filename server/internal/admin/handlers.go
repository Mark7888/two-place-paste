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
	"slices"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// sessionJSON is what the app learns about its session: only the CSRF token
// it must send back on every change. The session id itself stays in the
// HttpOnly cookie, out of reach of any script.
type sessionJSON struct {
	CSRF string `json:"csrf"`
}

// handleSession tells the app whether it is signed in, and hands it the CSRF
// token when it is.
func (s *Server) handleSession(w http.ResponseWriter, _ *http.Request, sess session) {
	writeJSON(w, http.StatusOK, sessionJSON{CSRF: sess.csrf})
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
		writeJSONError(w, http.StatusTooManyRequests, "Too many sign-in attempts. Wait a minute and try again.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.Password), s.password) != 1 {
		s.logger.WarnContext(r.Context(), "admin login failed", slog.String("client_ip", ip))
		s.audit(r, "login.failed", "", "")
		writeJSONError(w, http.StatusUnauthorized, "Incorrect password.")
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
	writeJSON(w, http.StatusOK, sessionJSON{CSRF: sess.csrf})
}

// handleLogout destroys the session server-side and clears the cookie. Without
// a session there is nothing to protect and it only clears the cookie; with
// one, it needs the CSRF header like any other change, so a cross-site request
// cannot sign the admin out.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := s.session(r); ok {
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
	w.WriteHeader(http.StatusNoContent)
}

// tokenJSON is one creation token as the token screen lists it (SPEC §4.4). It
// carries no clipboard data because the server holds none, and not the token
// itself either: an unused token is a live credential, so the row names it by
// its handle (handle.go) and the creation URL is fetched only when the admin
// asks for it (handleTokenLink).
type tokenJSON struct {
	Handle    string    `json:"handle"`
	Name      string    `json:"name"`
	Note      string    `json:"note"`
	Tag       string    `json:"tag"`
	CreatedAt time.Time `json:"created_at"`

	// Status is "unused", "used", "expired" or "group_deleted".
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at"`

	// GroupHandle names the group the token created, while that group exists.
	GroupHandle string `json:"group_handle,omitempty"`
}

// tokensJSON is the token screen: one page of the tokens, and the tags a new
// token can start its group under.
type tokensJSON struct {
	tablePage[tokenJSON]
	Tags []string `json:"tags"`
}

// createdTokenJSON is a token just minted. It carries its creation URL, so the
// screen can show the code straight away: this response is the one place
// besides the link endpoint the URL travels, and it is never in a URL itself.
type createdTokenJSON struct {
	tokenJSON
	URL string `json:"url"`
}

// tokenItem is a token's row with its value, which the list sorts ties by
// and never shows.
type tokenItem struct {
	row   tokenJSON
	value string
}

func (t tokenItem) json() tokenJSON { return t.row }

// statusOrder ranks token statuses: the ones still to be handed out first.
var statusOrder = map[string]int{"unused": 0, "expired": 1, "used": 2, "group_deleted": 3}

// statusText is how a status reads, for the search.
var statusText = map[string]string{"unused": "unused", "expired": "expired", "used": "used", "group_deleted": "group deleted"}

// tokenTable is the token list: searched by name, note, tag and status,
// newest first by default.
var tokenTable = table[tokenItem]{
	sorts: map[string]sortKey[tokenItem]{
		"name":    {cmp: func(a, b tokenItem) int { return byText(a.row.Name, b.row.Name) }},
		"created": {cmp: func(a, b tokenItem) int { return a.row.CreatedAt.Compare(b.row.CreatedAt) }, desc: true},
		"status":  {cmp: func(a, b tokenItem) int { return byNumber(statusOrder[a.row.Status], statusOrder[b.row.Status]) }},
		"expires": {cmp: func(a, b tokenItem) int { return byTime(a.row.ExpiresAt, b.row.ExpiresAt) }, desc: true},
	},
	defaultSort: "created",
	fields: func(t tokenItem) []string {
		return []string{t.row.Name, t.row.Note, t.row.Tag, statusText[t.row.Status]}
	},
	tie: func(a, b tokenItem) int {
		if c := byText(a.row.Name, b.row.Name); c != 0 {
			return c
		}
		return strings.Compare(a.value, b.value)
	},
}

func (s *Server) tokenView(t store.Token, now time.Time) tokenJSON {
	v := tokenJSON{
		Handle:    handleOf(t.Value),
		Name:      t.Name,
		Note:      t.Note,
		Tag:       t.Tag,
		CreatedAt: t.CreatedAt.UTC(),
		ExpiresAt: timeOrNil(t.ExpiresAt),
	}
	switch {
	case t.Used && t.GroupDeleted:
		v.Status = "group_deleted"
	case t.Used:
		v.Status = "used"
		v.GroupHandle = handleOf(t.GroupID)
	case t.Expired(now):
		v.Status = "expired"
	default:
		v.Status = "unused"
	}
	return v
}

// handleTokens returns one page of the creation tokens (SPEC §4.4). Beyond
// the list parameters it takes ?status=unused|used|expired; "used" includes
// tokens whose group has since been deleted.
func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request, _ session) {
	ctx := r.Context()
	tokens, err := s.store.ListTokens(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "list tokens", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The token list could not be loaded.")
		return
	}
	out := tokensJSON{Tags: []string{}}
	if tags, err := s.store.ListTags(ctx); err == nil {
		for _, t := range tags {
			out.Tags = append(out.Tags, t.Name)
		}
		slices.Sort(out.Tags)
	}

	query := r.URL.Query()
	status := query.Get("status")
	now := s.now()
	items := make([]tokenItem, 0, len(tokens))
	for _, t := range tokens {
		row := s.tokenView(t, now)
		switch status {
		case "unused", "expired":
			if row.Status != status {
				continue
			}
		case "used":
			if row.Status != "used" && row.Status != "group_deleted" {
				continue
			}
		}
		items = append(items, tokenItem{row: row, value: t.Value})
	}
	out.tablePage = mapPage(tokenTable.run(items, query), tokenItem.json)
	writeJSON(w, http.StatusOK, out)
}

// handleCreateToken mints a creation token (SPEC §3.1 step 1) and returns it
// with its creation URL, so the screen can show its code straight away. The
// URL travels in this response body only — never in a URL, where a credential
// would end up in the browser's history and in every access log on the way.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Name    string `json:"name"`
		Note    string `json:"note"`
		Tag     string `json:"tag"`
		Expires string `json:"expires"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, ok := validName(req.Name, maxTokenNameLen)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Give the token a name so you can tell it apart later.")
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > maxNoteLen {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Keep the note under %d characters.", maxNoteLen))
		return
	}
	if req.Tag != "" {
		if _, err := s.store.GetTag(r.Context(), req.Tag); err != nil {
			writeJSONError(w, http.StatusBadRequest, "That tag no longer exists.")
			return
		}
	}
	var expires time.Time
	switch req.Expires {
	case "", "never":
	case "1":
		expires = s.now().Add(24 * time.Hour)
	case "7":
		expires = s.now().Add(7 * 24 * time.Hour)
	case "30":
		expires = s.now().Add(30 * 24 * time.Hour)
	default:
		writeJSONError(w, http.StatusBadRequest, "Choose when the token expires from the list.")
		return
	}

	tok, err := s.store.CreateTokenWith(r.Context(), store.NewToken{Name: name, Note: note, Tag: req.Tag, ExpiresAt: expires})
	if err != nil {
		s.logger.ErrorContext(r.Context(), "create token", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The token could not be created.")
		return
	}
	// The token value itself is a credential and is never logged
	// (docs/internal/conventions.md §2).
	s.logger.InfoContext(r.Context(), "creation token issued", slog.String("token_name", tok.Name))
	s.audit(r, "token.create", tok.Name, expiryText(tok.ExpiresAt))
	writeJSON(w, http.StatusCreated, createdTokenJSON{tokenJSON: s.tokenView(tok, s.now()), URL: s.creationURL(tok.Value)})
}

// liveToken finds the unused, unexpired token a handle names: the only kind
// with a link or a code to show. On false a response has already been written.
func (s *Server) liveToken(w http.ResponseWriter, r *http.Request) (store.Token, bool) {
	tok, err := s.tokenByHandle(r.Context(), r.PathValue("handle"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.logger.ErrorContext(r.Context(), "read token", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The token could not be read.")
		return store.Token{}, false
	}
	if err != nil || tok.Used || tok.Expired(s.now()) {
		// A consumed or expired token creates nothing; handing out its link
		// would only invite someone to try.
		writeJSONError(w, http.StatusNotFound, "That token has no link any more: it was used, revoked or has expired.")
		return store.Token{}, false
	}
	return tok, true
}

// handleTokenLink returns an unused token's creation URL, for "Copy link" and
// "Show code". Asking for it is a deliberate act, one token at a time, so a
// screen of twenty tokens holds none of their credentials.
func (s *Server) handleTokenLink(w http.ResponseWriter, r *http.Request, _ session) {
	tok, ok := s.liveToken(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": s.creationURL(tok.Value)})
}

// handleRevokeToken deletes an unused creation token, so the URL that was
// handed out for it stops working. A used token is refused with a message
// rather than deleted: its group already exists, and removing the record
// would only hide which token created it.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request, _ session) {
	tok, err := s.tokenByHandle(r.Context(), r.PathValue("handle"))
	if err == nil {
		err = s.store.RevokeToken(r.Context(), tok.Value)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "That token no longer exists.")
		return
	case errors.Is(err, store.ErrTokenConsumed):
		writeJSONError(w, http.StatusConflict, "That token has already created a group, so there is nothing to revoke.")
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "revoke token", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The token could not be revoked.")
		return
	}
	// Logged by name only: the value is a credential (docs/internal/conventions.md §2).
	s.logger.InfoContext(r.Context(), "creation token revoked", slog.String("token_name", tok.Name))
	s.audit(r, "token.revoke", tok.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleTokenQR renders a token's creation URL as a QR code, so first-device
// onboarding is a scan rather than typing a URL on a phone (SPEC §4.4).
func (s *Server) handleTokenQR(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tok, err := s.tokenByHandle(r.Context(), r.PathValue("handle"))
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

// expiryText says when a token expires, for the audit log.
func expiryText(t time.Time) string {
	if t.IsZero() {
		return "never expires"
	}
	return "expires " + t.UTC().Format("2006-01-02 15:04 UTC")
}

// session returns the request's session, if it has a live one.
func (s *Server) session(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return session{}, false
	}
	return s.sessions.get(c.Value)
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
