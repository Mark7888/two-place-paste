package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Mark7888/two-place-paste/server/internal/config"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// limitInfo is the storage limit that applies to one group and where it comes
// from (docs/internal/plans/admin-group-management.md D3): the group's own
// override, its tag's, or the configured default.
type limitInfo struct {
	// Bytes is the limit; zero means the group has no limit of its own and
	// only the total applies.
	Bytes int64

	// Source is "group", "tag" or "default".
	Source string
}

// set reports whether the limit replaces the ledger's default.
func (l limitInfo) set() bool { return l.Source != "default" }

// effectiveLimit resolves default < tag < group: the first enabled override
// wins, counting from the group.
func (s *Server) effectiveLimit(g store.Group, tags map[string]store.Tag) limitInfo {
	if g.Limit.Enabled {
		return limitInfo{Bytes: g.Limit.Bytes, Source: "group"}
	}
	if t, ok := tags[g.Tag]; ok && g.Tag != "" && t.Limit.Enabled {
		return limitInfo{Bytes: t.Limit.Bytes, Source: "tag"}
	}
	return limitInfo{Bytes: s.dashboard.GroupLimit, Source: "default"}
}

// shownLimit is the cap a group actually runs into: its own limit, or the
// total when that is lower or the group has none. Zero means no cap at all.
func (s *Server) shownLimit(l limitInfo) int64 {
	limit, total := l.Bytes, s.dashboard.TotalLimit
	if total > 0 && (limit == 0 || total < limit) {
		return total
	}
	return limit
}

// tagMap reads every tag, keyed by name.
func (s *Server) tagMap(ctx context.Context) (map[string]store.Tag, error) {
	tags, err := s.store.ListTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	m := make(map[string]store.Tag, len(tags))
	for _, t := range tags {
		m[t.Name] = t
	}
	return m, nil
}

// applyLimits pushes the effective limit of each group to the ledger. The
// store holds what the admin chose; the ledger holds what uploads are
// checked against, and this is the one place that turns one into the other.
func (s *Server) applyLimits(ctx context.Context, groups []store.Group, tags map[string]store.Tag) error {
	if s.limits == nil {
		return nil
	}
	var errs []error
	for _, g := range groups {
		l := s.effectiveLimit(g, tags)
		if err := s.limits.SetGroupLimit(ctx, g.ID, l.Bytes, l.set()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SyncStorageLimits applies every group's effective storage limit to the
// ledger. The server calls it once at startup, so the ledger agrees with the
// store even after a crash between the two writes of an edit, or a restore of
// one without the other.
func (s *Server) SyncStorageLimits(ctx context.Context) error {
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return fmt.Errorf("sync storage limits: %w", err)
	}
	tags, err := s.tagMap(ctx)
	if err != nil {
		return fmt.Errorf("sync storage limits: %w", err)
	}
	if err := s.applyLimits(ctx, groups, tags); err != nil {
		return fmt.Errorf("sync storage limits: %w", err)
	}
	return nil
}

// applyGroupLimit re-resolves one group's limit after an edit to it.
func (s *Server) applyGroupLimit(ctx context.Context, groupID string) error {
	g, err := s.store.GetGroup(ctx, groupID)
	if err != nil {
		return fmt.Errorf("apply storage limit of group %s: %w", groupID, err)
	}
	tags, err := s.tagMap(ctx)
	if err != nil {
		return fmt.Errorf("apply storage limit of group %s: %w", groupID, err)
	}
	return s.applyLimits(ctx, []store.Group{g}, tags)
}

// groupLabel is how a group is named in the audit log and in notices: its
// display name and enough of its id to tell two of the same name apart.
func groupLabel(g store.Group) string {
	return g.Name + " (" + shortID(g.ID) + ")"
}

func groupPath(id string) string { return "/admin/groups/" + url.PathEscape(id) }

// validName trims and checks a display name: not empty, no control
// characters, at most max bytes.
func validName(raw string, maxLen int) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > maxLen || !utf8.ValidString(name) {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return name, true
}

// validTagName checks a tag name: a letter or digit first, then letters,
// digits, spaces, '.', '_' or '-'. The first-character rule keeps "-", the
// dashboard's "untagged" filter, from ever naming a tag.
func validTagName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxTagNameLen || !utf8.ValidString(name) {
		return "", false
	}
	for i, r := range name {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r)
		if i > 0 {
			ok = ok || r == ' ' || r == '.' || r == '_' || r == '-'
		}
		if !ok {
			return "", false
		}
	}
	return name, true
}

// parseLimitForm reads a storage limit form: the "limit_on" checkbox and the
// "limit" size. An empty size keeps the stored bytes, so unticking the box
// does not demand a value; a size is checked against the total limit.
func (s *Server) parseLimitForm(r *http.Request, current store.LimitOverride) (store.LimitOverride, string) {
	out := store.LimitOverride{Bytes: current.Bytes, Enabled: r.PostFormValue("limit_on") != ""}
	raw := strings.TrimSpace(r.PostFormValue("limit"))
	if raw != "" {
		n, err := config.ParseByteSize(raw)
		if err != nil || n < 0 {
			return current, "That is not a size. Use e.g. “5 GiB”, “500 MB” or 0 for no limit of its own."
		}
		out.Bytes = n
	}
	if out.Enabled && s.dashboard.TotalLimit > 0 && out.Bytes > s.dashboard.TotalLimit {
		return current, fmt.Sprintf("The limit cannot be above the server's total limit of %s.", humanBytes(s.dashboard.TotalLimit))
	}
	return out, ""
}

// sizeInput renders a byte count the way the limit field accepts it back.
func sizeInput(n int64) string {
	switch {
	case n == 0:
		return "0"
	case n%(1<<40) == 0:
		return fmt.Sprintf("%d TiB", n>>40)
	case n%(1<<30) == 0:
		return fmt.Sprintf("%d GiB", n>>30)
	case n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// groupDetailView is the group page. Like the dashboard it holds counts,
// sizes and device names, never content.
type groupDetailView struct {
	groupView

	Tags []string

	LimitOn     bool
	LimitInput  string
	LimitSource string
	DefaultText string
	TagLimit    string

	DeviceRows []deviceView

	CanPurge bool
}

type deviceView struct {
	Name     string
	ShortID  string
	Created  string
	LastSeen string
	Online   bool
}

// handleGroup renders one group's page.
func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/admin/login")
		return
	}
	s.renderGroup(w, r, sess, r.PathValue("id"), http.StatusOK, "")
}

func (s *Server) renderGroup(w http.ResponseWriter, r *http.Request, sess session, id string, status int, msg string) {
	ctx := r.Context()
	data := s.page(sess, "Group", "dashboard")
	data.Error = msg

	g, err := s.store.GetGroup(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		data.Title = "No such group"
		data.Error = "That group does not exist. It may have been deleted."
		s.render(w, r, http.StatusNotFound, "group.html", data)
		return
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "read group", "error", err)
		data.Error = "The group could not be loaded."
		s.render(w, r, http.StatusInternalServerError, "group.html", data)
		return
	}
	tags, err := s.tagMap(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "list tags", "error", err)
		data.Error = "The tags could not be loaded."
	}
	settings := s.settings(ctx)
	now := s.now()

	var problems []string
	problem := func(what string, err error) {
		s.logger.ErrorContext(ctx, "group page: "+what, "error", err)
		problems = append(problems, "Could not read "+what+".")
	}
	row, devices := s.groupRow(ctx, g, tags, settings, now, problem)

	v := &groupDetailView{
		groupView:   row,
		LimitOn:     g.Limit.Enabled,
		LimitInput:  sizeInput(g.Limit.Bytes),
		LimitSource: row.LimitSource,
		DefaultText: limitText(s.dashboard.GroupLimit),
		CanPurge:    s.purger != nil,
	}
	if t, ok := tags[g.Tag]; ok && t.Limit.Enabled {
		v.TagLimit = limitText(t.Limit.Bytes)
	}
	for name := range tags {
		v.Tags = append(v.Tags, name)
	}
	slices.Sort(v.Tags)

	var online map[string]bool
	if s.live != nil {
		online = s.live.OnlineDevices(g.ID)
	}
	for _, d := range devices {
		v.DeviceRows = append(v.DeviceRows, deviceView{
			Name:     d.Name,
			ShortID:  shortID(d.ID),
			Created:  d.CreatedAt.UTC().Format("2006-01-02"),
			LastSeen: ago(now, d.LastSeen, "never"),
			Online:   online[d.ID],
		})
	}

	data.Title = g.Name
	data.Group = v
	data.Problems = problems
	s.render(w, r, status, "group.html", data)
}

// handleRenameGroup sets a group's display name and tells its devices.
func (s *Server) handleRenameGroup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	name, ok := validName(r.PostFormValue("name"), maxGroupNameLen)
	if !ok {
		s.renderGroup(w, r, sess, id, http.StatusBadRequest, fmt.Sprintf("Give the group a name of at most %d characters.", maxGroupNameLen))
		return
	}
	ctx := r.Context()
	before, err := s.store.GetGroup(ctx, id)
	if err == nil {
		err = s.store.RenameGroup(ctx, id, name)
	}
	if !s.groupEditDone(w, r, sess, id, err, "rename group") {
		return
	}
	if s.live != nil {
		s.live.AnnounceRename(ctx, id, name)
	}
	s.audit(r, "group.rename", groupLabel(before), "to "+name)
	s.sessions.setNotice(sess.id, "Group renamed. Its devices show the new name.")
	s.redirect(w, r, groupPath(id))
}

// handleSetGroupTag puts a group under a tag, or under none.
func (s *Server) handleSetGroupTag(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	tag := r.PostFormValue("tag")
	ctx := r.Context()
	g, err := s.store.GetGroup(ctx, id)
	if err == nil {
		err = s.store.SetGroupTag(ctx, id, tag)
	}
	if errors.Is(err, store.ErrNotFound) && g.ID != "" {
		s.renderGroup(w, r, sess, id, http.StatusBadRequest, "That tag no longer exists.")
		return
	}
	if !s.groupEditDone(w, r, sess, id, err, "set group tag") {
		return
	}
	if err := s.applyGroupLimit(ctx, id); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limit", "group_id", id, "error", err)
	}
	detail := "untagged"
	if tag != "" {
		detail = "tagged " + tag
	}
	s.audit(r, "group.tag", groupLabel(g), detail)
	s.sessions.setNotice(sess.id, "Tag saved.")
	s.redirect(w, r, groupPath(id))
}

// handleSetGroupLimit stores a group's own storage limit override and applies
// whatever limit now wins.
func (s *Server) handleSetGroupLimit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	g, err := s.store.GetGroup(ctx, id)
	if err != nil {
		s.groupEditDone(w, r, sess, id, err, "read group")
		return
	}
	limit, msg := s.parseLimitForm(r, g.Limit)
	if msg != "" {
		s.renderGroup(w, r, sess, id, http.StatusBadRequest, msg)
		return
	}
	if !s.groupEditDone(w, r, sess, id, s.store.SetGroupLimit(ctx, id, limit), "set group storage limit") {
		return
	}
	if err := s.applyGroupLimit(ctx, id); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limit", "group_id", id, "error", err)
		s.renderGroup(w, r, sess, id, http.StatusInternalServerError, "The limit was saved but could not be applied yet. It will be at the next restart.")
		return
	}
	s.audit(r, "group.limit", groupLabel(g), limitAuditText(limit))
	s.sessions.setNotice(sess.id, "Storage limit saved.")
	s.redirect(w, r, groupPath(id))
}

func limitAuditText(l store.LimitOverride) string {
	if !l.Enabled {
		return "own limit off (kept " + limitText(l.Bytes) + ")"
	}
	return "own limit " + limitText(l.Bytes)
}

// groupEditDone turns a store error from a group edit into a response, and
// reports whether the caller should carry on.
func (s *Server) groupEditDone(w http.ResponseWriter, r *http.Request, sess session, id string, err error, what string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNotFound):
		s.renderGroup(w, r, sess, id, http.StatusNotFound, "")
	default:
		s.logger.ErrorContext(r.Context(), what, "group_id", id, "error", err)
		s.renderGroup(w, r, sess, id, http.StatusInternalServerError, "The change could not be saved.")
	}
	return false
}

// confirmView is a confirmation page for an action that cannot be undone
// (plan D14). It names the group the action touches and what it frees.
type confirmView struct {
	Heading string
	Lede    string
	Action  string
	Button  string
	Back    string
	Group   groupView
	Total   string
}

// handlePurgeConfirm and handleDeleteConfirm render the confirmation pages.
func (s *Server) handlePurgeConfirm(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/admin/login")
		return
	}
	id := r.PathValue("id")
	s.renderConfirm(w, r, sess, id, confirmView{
		Heading: "Purge entries",
		Lede: "Every entry this group holds is deleted now, before its 24 hours are up. " +
			"Its devices keep working and their own clipboards are not touched; they only lose the shared history.",
		Action: groupPath(id) + "/purge",
		Button: "Purge entries",
		Back:   groupPath(id),
	})
}

func (s *Server) handleDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/admin/login")
		return
	}
	id := r.PathValue("id")
	s.renderConfirm(w, r, sess, id, confirmView{
		Heading: "Delete group",
		Lede: "The group, its devices and every entry it holds are deleted. " +
			"Each device is disconnected and told its group was deleted; to use this server again it needs a new creation token. " +
			"This cannot be undone.",
		Action: groupPath(id) + "/delete",
		Button: "Delete group",
		Back:   groupPath(id),
	})
}

func (s *Server) renderConfirm(w http.ResponseWriter, r *http.Request, sess session, id string, v confirmView) {
	ctx := r.Context()
	data := s.page(sess, v.Heading, "dashboard")
	tags, err := s.tagMap(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "list tags", "error", err)
	}
	settings := s.settings(ctx)
	now := s.now()
	var problems []string
	problem := func(what string, err error) {
		s.logger.ErrorContext(ctx, "confirm page: "+what, "error", err)
		problems = append(problems, "Could not read "+what+".")
	}

	g, err := s.store.GetGroup(ctx, id)
	if err != nil {
		// Gone already, or never was: nothing to confirm.
		s.sessions.setNotice(sess.id, "That group no longer exists.")
		s.redirect(w, r, "/admin/")
		return
	}
	row, _ := s.groupRow(ctx, g, tags, settings, now, problem)
	v.Group = row
	v.Total = humanBytes(row.UsedBytes)
	data.Title = v.Heading
	data.Confirm = &v
	data.Problems = problems
	s.render(w, r, http.StatusOK, "confirm.html", data)
}

// handlePurgeGroup deletes a group's entries now.
func (s *Server) handlePurgeGroup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	if s.purger == nil {
		s.renderGroup(w, r, sess, id, http.StatusNotImplemented, "Purging is not available on this server.")
		return
	}
	g, err := s.store.GetGroup(ctx, id)
	if !s.groupEditDone(w, r, sess, id, err, "read group") {
		return
	}
	res, err := s.purger.PurgeGroup(ctx, id)
	if err != nil {
		s.logger.ErrorContext(ctx, "purge group", "group_id", id, "error", err)
		s.renderGroup(w, r, sess, id, http.StatusInternalServerError, "The purge did not finish. Some entries may be left; try again.")
		return
	}
	s.audit(r, "group.purge", groupLabel(g), fmt.Sprintf("%d entries, %s", res.Entries, humanBytes(res.Bytes)))
	s.sessions.setNotice(sess.id, fmt.Sprintf("Purged %d %s, %s freed.", res.Entries, plural(res.Entries, "entry", "entries"), humanBytes(res.Bytes)))
	s.redirect(w, r, groupPath(id))
}

// handleDeleteGroup deletes one group. Groups are deleted one at a time, from
// their own page: each deletion cuts a set of devices off for good, so each
// is a decision about one group.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	name, err := s.deleteGroup(r, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.sessions.setNotice(sess.id, "Nothing was deleted: that group no longer exists.")
	case err != nil:
		s.renderGroup(w, r, sess, id, http.StatusInternalServerError, "The group could not be deleted. Check the server log.")
		return
	default:
		s.sessions.setNotice(sess.id, "Deleted the group “"+name+"”.")
	}
	s.redirect(w, r, "/admin/")
}

// deleteGroup removes a group: the persistent zone first, which is what cuts
// its devices off, then their sockets, then its entries
// (docs/internal/plans/admin-group-management.md D6). A purge failure after
// the group is gone leaves only entries nobody can read, which expire within
// 24 hours anyway.
//
// It returns the deleted group's name.
func (s *Server) deleteGroup(r *http.Request, id string) (string, error) {
	ctx := r.Context()
	del, err := s.store.DeleteGroup(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.logger.ErrorContext(ctx, "delete group", "group_id", id, "error", err)
		}
		return "", fmt.Errorf("delete group %s: %w", id, err)
	}
	if s.live != nil {
		s.live.CloseGroup(id)
	}
	detail := fmt.Sprintf("%d %s", len(del.DeviceIDs), plural(len(del.DeviceIDs), "device", "devices"))
	if s.purger != nil {
		res, err := s.purger.PurgeGroup(context.WithoutCancel(ctx), id)
		if err != nil {
			s.logger.ErrorContext(ctx, "purge deleted group; its entries expire on their own", "group_id", id, "error", err)
		} else {
			detail += fmt.Sprintf(", %d entries, %s", res.Entries, humanBytes(res.Bytes))
		}
	}
	if s.limits != nil {
		if err := s.limits.SetGroupLimit(ctx, id, 0, false); err != nil {
			s.logger.WarnContext(ctx, "drop storage limit of deleted group", "group_id", id, "error", err)
		}
	}
	s.logger.InfoContext(ctx, "group deleted by admin",
		slog.String("group_id", id), slog.Int("devices", len(del.DeviceIDs)))
	s.audit(r, "group.delete", groupLabel(del.Group), detail)
	return del.Group.Name, nil
}

// lastActive is when any device of the group last connected, or when the
// group was created if none ever has.
func lastActive(g store.Group, devices []store.Device) time.Time {
	last := g.CreatedAt
	for _, d := range devices {
		if d.LastSeen.After(last) {
			last = d.LastSeen
		}
	}
	return last
}

func isStale(now, last time.Time, after time.Duration) bool {
	return after > 0 && !last.IsZero() && now.Sub(last) > after
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
