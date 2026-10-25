package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// auditLabels names each audit action in plain words, in the order the
// action filter lists them. They live here rather than in the app so that the
// search matches what the screen shows.
var auditLabels = []struct{ action, label string }{
	{"login", "Signed in"},
	{"login.failed", "Sign-in failed"},
	{"login.limited", "Sign-in rate limited"},
	{"logout", "Signed out"},
	{"token.create", "Token created"},
	{"token.revoke", "Token revoked"},
	{"group.rename", "Group renamed"},
	{"group.tag", "Group tag changed"},
	{"group.limit", "Group storage limit changed"},
	{"group.store", "Group store turned on or off"},
	{"group.storelimit", "Group store limit changed"},
	{"group.storepurge", "Group store purged"},
	{"group.purge", "Group entries purged"},
	{"group.delete", "Group deleted"},
	{"tag.create", "Tag created"},
	{"tag.limit", "Tag storage limit changed"},
	{"tag.storelimit", "Tag store limit changed"},
	{"tag.delete", "Tag deleted"},
	{"settings.update", "Settings changed"},
	{"sweep", "Sweep requested"},
}

// auditLabel names an action. One this build does not know, from a newer
// relay sharing the Redis, is shown as it is stored.
func auditLabel(action string) string {
	for _, l := range auditLabels {
		if l.action == action {
			return l.label
		}
	}
	return action
}

// auditJSON is one row of the audit screen.
type auditJSON struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Label  string    `json:"label"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
}

// auditListJSON is the audit screen: one page of the log, how much the
// server keeps, and the actions the filter offers.
type auditListJSON struct {
	tablePage[auditJSON]
	Kept    int               `json:"kept"`
	Actions []auditActionJSON `json:"actions"`
}

type auditActionJSON struct {
	Action string `json:"action"`
	Label  string `json:"label"`
}

// auditTable is the audit log: searched by action, target, details and
// address, newest first by default. The log comes newest first and the sort
// is stable, so entries the sort column calls equal stay newest first.
var auditTable = table[auditJSON]{
	sorts: map[string]sortKey[auditJSON]{
		"when":   {cmp: func(a, b auditJSON) int { return a.At.Compare(b.At) }, desc: true},
		"action": {cmp: func(a, b auditJSON) int { return byText(a.Label, b.Label) }},
		"target": {cmp: func(a, b auditJSON) int { return byText(a.Target, b.Target) }},
		"ip":     {cmp: func(a, b auditJSON) int { return byText(a.IP, b.IP) }},
	},
	defaultSort: "when",
	fields: func(e auditJSON) []string {
		return []string{e.Action, e.Label, e.Target, e.Detail, e.IP}
	},
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

// handleAudit returns one page of the audit log. Beyond the list parameters
// it takes ?action=name to show one kind of entry.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, _ session) {
	entries, err := s.store.ListAudit(r.Context(), store.AuditLimit)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "list audit entries", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The audit log could not be loaded.")
		return
	}
	query := r.URL.Query()
	action := query.Get("action")
	rows := make([]auditJSON, 0, len(entries))
	for _, e := range entries {
		if action != "" && e.Action != action {
			continue
		}
		rows = append(rows, auditJSON{At: e.At.UTC(), Action: e.Action, Label: auditLabel(e.Action), Target: e.Target, Detail: e.Detail, IP: e.IP})
	}
	out := auditListJSON{
		tablePage: auditTable.run(rows, query),
		Kept:      store.AuditLimit,
		Actions:   make([]auditActionJSON, 0, len(auditLabels)),
	}
	for _, l := range auditLabels {
		out.Actions = append(out.Actions, auditActionJSON{Action: l.action, Label: l.label})
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
