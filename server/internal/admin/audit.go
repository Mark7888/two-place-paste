package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// auditPageSize is how many audit entries the audit screen shows.
const auditPageSize = 200

// auditJSON is one row of the audit screen. The app names each action in
// plain words; an action it does not know is shown as it is stored.
type auditJSON struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
}

type auditListJSON struct {
	Entries []auditJSON `json:"entries"`

	// Kept is how many entries the server keeps in all.
	Kept int `json:"kept"`
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

// handleAudit returns the newest audit entries.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, _ session) {
	entries, err := s.store.ListAudit(r.Context(), auditPageSize)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "list audit entries", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The audit log could not be loaded.")
		return
	}
	out := auditListJSON{Entries: make([]auditJSON, 0, len(entries)), Kept: store.AuditLimit}
	for _, e := range entries {
		out.Entries = append(out.Entries, auditJSON{At: e.At.UTC(), Action: e.Action, Target: e.Target, Detail: e.Detail, IP: e.IP})
	}
	writeJSON(w, http.StatusOK, out)
}

// sweepResultJSON says whether the sweep asked for was queued, or one was
// already waiting.
type sweepResultJSON struct {
	Queued bool `json:"queued"`
}

// handleSweep asks for a blob sweep now.
func (s *Server) handleSweep(w http.ResponseWriter, r *http.Request, _ session) {
	if s.sweep == nil {
		writeJSONError(w, http.StatusNotImplemented, "Sweeping is not available on this server.")
		return
	}
	queued := s.sweep()
	s.audit(r, "sweep", "", "")
	writeJSON(w, http.StatusOK, sweepResultJSON{Queued: queued})
}
