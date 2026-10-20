package admin

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// auditPageSize is how many audit entries the audit screen shows.
const auditPageSize = 200

// auditLabels names each audit action in plain words.
var auditLabels = map[string]string{
	"login":           "Signed in",
	"login.failed":    "Sign-in failed",
	"login.limited":   "Sign-in rate limited",
	"logout":          "Signed out",
	"token.create":    "Token created",
	"token.revoke":    "Token revoked",
	"group.rename":    "Group renamed",
	"group.tag":       "Group tag changed",
	"group.limit":     "Group storage limit changed",
	"group.purge":     "Group entries purged",
	"group.delete":    "Group deleted",
	"tag.create":      "Tag created",
	"tag.limit":       "Tag storage limit changed",
	"tag.delete":      "Tag deleted",
	"settings.update": "Settings changed",
	"sweep":           "Sweep requested",
}

// auditView is one row of the audit screen.
type auditView struct {
	At     string
	Action string
	Target string
	Detail string
	IP     string
}

// audit records an admin action. The entry holds names, counts and the
// client address — never a token value, a password or a cookie. A failure
// to record is logged and does not undo the action.
func (s *Server) audit(r *http.Request, action, target, detail string) {
	s.auditCtx(r.Context(), clientIP(r), action, target, detail)
}

func (s *Server) auditCtx(ctx context.Context, ip, action, target, detail string) {
	err := s.store.AppendAudit(context.WithoutCancel(ctx), store.AuditEntry{
		At: s.now(), Action: action, Target: target, Detail: detail, IP: ip,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "record audit entry", "action", action, "error", err)
	}
}

// handleAudit renders the audit screen.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/admin/login")
		return
	}
	data := s.page(sess, "Audit log", "audit")
	entries, err := s.store.ListAudit(r.Context(), auditPageSize)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "list audit entries", "error", err)
		data.Error = "The audit log could not be loaded."
		s.render(w, r, http.StatusInternalServerError, "audit.html", data)
		return
	}
	now := s.now()
	for _, e := range entries {
		label := auditLabels[e.Action]
		if label == "" {
			label = e.Action
		}
		data.Audit = append(data.Audit, auditView{
			At:     e.At.UTC().Format("2006-01-02 15:04 UTC") + " · " + ago(now, e.At, ""),
			Action: label,
			Target: e.Target,
			Detail: e.Detail,
			IP:     e.IP,
		})
	}
	data.AuditLimit = strconv.Itoa(store.AuditLimit)
	s.render(w, r, http.StatusOK, "audit.html", data)
}

// handleSweep asks for a blob sweep now.
func (s *Server) handleSweep(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	if s.sweep == nil {
		s.sessions.setNotice(sess.id, "Sweeping is not available on this server.")
		s.redirect(w, r, "/admin/")
		return
	}
	if s.sweep() {
		s.sessions.setNotice(sess.id, "Sweep started. Reload in a moment to see its result.")
	} else {
		s.sessions.setNotice(sess.id, "A sweep is already waiting to run.")
	}
	s.audit(r, "sweep", "", "")
	s.redirect(w, r, "/admin/")
}
