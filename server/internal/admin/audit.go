package admin

import (
	"context"
	"net/http"
	"net/url"
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
	{"apikey.create", "API key created"},
	{"apikey.revoke", "API key revoked"},
	{"apikey.limited", "API key attempts rate limited"},
}

// actorLabel says who made an audited change, in words.
func actorLabel(e store.AuditEntry) string {
	switch e.Actor {
	case actorAPIKey:
		if e.ActorName == "" {
			return "API key " + e.ActorID
		}
		return "API key “" + e.ActorName + "” (" + e.ActorID + ")"
	case actorCLI:
		return "Command line"
	default:
		return "Admin page"
	}
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

	// Actor is "" for the admin page, "api_key" or "cli", and ActorID and
	// ActorName name the API key. By says the same in words.
	Actor     string `json:"actor"`
	ActorID   string `json:"actor_id"`
	ActorName string `json:"actor_name"`
	By        string `json:"by"`
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
		return []string{e.Action, e.Label, e.Target, e.Detail, e.IP, e.By}
	},
}

// audit records an admin action by the request's actor. The entry holds
// names, counts, the client address and the actor — never a token value, a
// password, a cookie or an API key. A failure to record is logged and does
// not undo the action.
//
// A request without an actor is a sign-in or sign-out on the admin page,
// which happens before or outside a session.
func (s *Server) audit(r *http.Request, action, target, detail string) {
	a, ok := actorFrom(r.Context())
	if !ok {
		a = actor{ip: clientIP(r)}
	}
	s.record(r.Context(), a, action, target, detail)
}

// auditCtx records an action by the actor ctx carries.
func (s *Server) auditCtx(ctx context.Context, action, target, detail string) {
	a, _ := actorFrom(ctx)
	s.record(ctx, a, action, target, detail)
}

func (s *Server) record(ctx context.Context, a actor, action, target, detail string) {
	err := s.store.AppendAudit(context.WithoutCancel(ctx), store.AuditEntry{
		At: s.now(), Action: action, Target: target, Detail: detail, IP: a.ip,
		Actor: a.kind, ActorID: a.keyID, ActorName: a.keyName,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "record audit entry", "action", action, "error", err)
	}
}

// auditListView builds one page of the audit log. Beyond the list
// parameters it takes action=name to show one kind of entry.
func (s *Server) auditListView(ctx context.Context, query url.Values) (auditListJSON, error) {
	entries, err := s.store.ListAudit(ctx, store.AuditLimit)
	if err != nil {
		s.logger.ErrorContext(ctx, "list audit entries", "error", err)
		return auditListJSON{}, failed("The audit log could not be loaded.")
	}
	action := query.Get("action")
	rows := make([]auditJSON, 0, len(entries))
	for _, e := range entries {
		if action != "" && e.Action != action {
			continue
		}
		rows = append(rows, auditJSON{
			At: e.At.UTC(), Action: e.Action, Label: auditLabel(e.Action), Target: e.Target, Detail: e.Detail, IP: e.IP,
			Actor: e.Actor, ActorID: e.ActorID, ActorName: e.ActorName, By: actorLabel(e),
		})
	}
	out := auditListJSON{
		tablePage: auditTable.run(rows, query),
		Kept:      store.AuditLimit,
		Actions:   make([]auditActionJSON, 0, len(auditLabels)),
	}
	for _, l := range auditLabels {
		out.Actions = append(out.Actions, auditActionJSON{Action: l.action, Label: l.label})
	}
	return out, nil
}

// handleAudit returns one page of the audit log.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, _ session) {
	out, err := s.auditListView(r.Context(), r.URL.Query())
	if err != nil {
		writeOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sweepResultJSON says whether the sweep asked for was queued, or one was
// already waiting.
type sweepResultJSON struct {
	Queued bool `json:"queued"`
}

// sweepNow asks for a blob sweep now and reports whether one was queued.
func (s *Server) sweepNow(ctx context.Context) (bool, error) {
	if s.sweep == nil {
		return false, unavailable("Sweeping is not available on this server.")
	}
	queued := s.sweep()
	s.auditCtx(ctx, "sweep", "", "")
	return queued, nil
}

// handleSweep asks for a blob sweep now.
func (s *Server) handleSweep(w http.ResponseWriter, r *http.Request, _ session) {
	queued, err := s.sweepNow(r.Context())
	if err != nil {
		writeOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sweepResultJSON{Queued: queued})
}
