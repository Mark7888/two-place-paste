package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

// groupLabel is how a group is named in the audit log: its display name and
// enough of its id to tell two of the same name apart.
func groupLabel(g store.Group) string {
	return g.Name + " (" + shortID(g.ID) + ")"
}

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
// groups screen's "untagged" filter, from ever naming a tag.
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

// parseLimit reads a storage limit change: the checkbox and the size. An
// empty size keeps the stored bytes, so unticking the box does not demand a
// value; a size is checked against the total limit. On failure it returns the
// message to show.
func (s *Server) parseLimit(req limitRequest, current store.LimitOverride) (store.LimitOverride, string) {
	out := store.LimitOverride{Bytes: current.Bytes, Enabled: req.Enabled}
	raw := strings.TrimSpace(req.Size)
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

// groupDetailJSON is the group page. Like the dashboard it holds counts,
// sizes and device names, never content.
type groupDetailJSON struct {
	Group   groupJSON    `json:"group"`
	Devices []deviceJSON `json:"devices"`

	// OwnLimit is the group's own override, on or off; TagLimitBytes its
	// tag's limit when that is on; DefaultLimitBytes the configured default.
	OwnLimit          limitJSON `json:"own_limit"`
	TagLimitBytes     *int64    `json:"tag_limit_bytes"`
	DefaultLimitBytes int64     `json:"default_limit_bytes"`

	// Tags are the tags the group can be put under.
	Tags []string `json:"tags"`

	// Store is the group's store, or nil when the relay has none.
	Store *groupStoreJSON `json:"store"`

	CanPurge bool `json:"can_purge"`

	Problems []string `json:"problems"`
}

type deviceJSON struct {
	ShortID   string     `json:"short_id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	LastSeen  *time.Time `json:"last_seen"`
	Online    bool       `json:"online"`
}

// handleGroup returns one group's page.
func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request, _ session) {
	ctx := r.Context()
	g, err := s.groupByHandle(ctx, r.PathValue("handle"))
	if !s.groupFound(w, r, g.ID, err, "read group") {
		return
	}
	probs := s.problems(ctx, "group page")
	tags, err := s.tagMap(ctx)
	if err != nil {
		probs.add("the tags", err)
	}
	settings := s.settings(ctx)
	now := s.now()

	row, devices := s.groupRow(ctx, g, tags, settings, now, probs)
	v := groupDetailJSON{
		Group:             row,
		Devices:           make([]deviceJSON, 0, len(devices)),
		OwnLimit:          limitJSON{Enabled: g.Limit.Enabled, Bytes: g.Limit.Bytes},
		DefaultLimitBytes: s.dashboard.GroupLimit,
		Tags:              make([]string, 0, len(tags)),
		CanPurge:          s.purger != nil,
	}
	if t, ok := tags[g.Tag]; ok && t.Limit.Enabled {
		b := t.Limit.Bytes
		v.TagLimitBytes = &b
	}
	v.Store = s.groupStore(ctx, g, tags, probs)
	for name := range tags {
		v.Tags = append(v.Tags, name)
	}
	slices.Sort(v.Tags)

	var online map[string]bool
	if s.live != nil {
		online = s.live.OnlineDevices(g.ID)
	}
	for _, d := range devices {
		v.Devices = append(v.Devices, deviceJSON{
			ShortID:   shortID(d.ID),
			Name:      d.Name,
			CreatedAt: d.CreatedAt.UTC(),
			LastSeen:  timeOrNil(d.LastSeen),
			Online:    online[d.ID],
		})
	}
	v.Problems = probs.list
	writeJSON(w, http.StatusOK, v)
}

// handleRenameGroup sets a group's display name and tells its devices.
func (s *Server) handleRenameGroup(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, ok := validName(req.Name, maxGroupNameLen)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Give the group a name of at most %d characters.", maxGroupNameLen))
		return
	}
	ctx := r.Context()
	before, err := s.groupByHandle(ctx, r.PathValue("handle"))
	id := before.ID
	if err == nil {
		err = s.store.RenameGroup(ctx, id, name)
	}
	if !s.groupFound(w, r, id, err, "rename group") {
		return
	}
	if s.live != nil {
		s.live.AnnounceRename(ctx, id, name)
	}
	s.audit(r, "group.rename", groupLabel(before), "to "+name)
	w.WriteHeader(http.StatusNoContent)
}

// handleSetGroupTag puts a group under a tag, or under none.
func (s *Server) handleSetGroupTag(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Tag string `json:"tag"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	g, err := s.groupByHandle(ctx, r.PathValue("handle"))
	id := g.ID
	if err == nil {
		err = s.store.SetGroupTag(ctx, id, req.Tag)
	}
	if errors.Is(err, store.ErrNotFound) && g.ID != "" {
		writeJSONError(w, http.StatusBadRequest, "That tag no longer exists.")
		return
	}
	if !s.groupFound(w, r, id, err, "set group tag") {
		return
	}
	if err := s.applyGroupLimit(ctx, id); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limit", "group_id", id, "error", err)
	}
	detail := "untagged"
	if req.Tag != "" {
		detail = "tagged " + req.Tag
	}
	s.audit(r, "group.tag", groupLabel(g), detail)
	w.WriteHeader(http.StatusNoContent)
}

// handleSetGroupLimit stores a group's own storage limit override and applies
// whatever limit now wins.
func (s *Server) handleSetGroupLimit(w http.ResponseWriter, r *http.Request, _ session) {
	var req limitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	g, err := s.groupByHandle(ctx, r.PathValue("handle"))
	id := g.ID
	if !s.groupFound(w, r, id, err, "read group") {
		return
	}
	limit, msg := s.parseLimit(req, g.Limit)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}
	if !s.groupFound(w, r, id, s.store.SetGroupLimit(ctx, id, limit), "set group storage limit") {
		return
	}
	if err := s.applyGroupLimit(ctx, id); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limit", "group_id", id, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The limit was saved but could not be applied yet. It will be at the next restart.")
		return
	}
	s.audit(r, "group.limit", groupLabel(g), limitAuditText(limit))
	w.WriteHeader(http.StatusNoContent)
}

func limitAuditText(l store.LimitOverride) string {
	if !l.Enabled {
		return "own limit off (kept " + limitText(l.Bytes) + ")"
	}
	return "own limit " + limitText(l.Bytes)
}

// groupFound turns a store error from reading or editing a group into a
// response, and reports whether the caller should carry on.
func (s *Server) groupFound(w http.ResponseWriter, r *http.Request, id string, err error, what string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "That group does not exist. It may have been deleted.")
	default:
		s.logger.ErrorContext(r.Context(), what, "group_id", id, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The change could not be saved.")
	}
	return false
}

// purgeJSON is what a purge freed.
type purgeJSON struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
}

// handlePurgeGroup deletes a group's entries now.
func (s *Server) handlePurgeGroup(w http.ResponseWriter, r *http.Request, _ session) {
	ctx := r.Context()
	if s.purger == nil {
		writeJSONError(w, http.StatusNotImplemented, "Purging is not available on this server.")
		return
	}
	g, err := s.groupByHandle(ctx, r.PathValue("handle"))
	id := g.ID
	if !s.groupFound(w, r, id, err, "read group") {
		return
	}
	res, err := s.purger.PurgeGroup(ctx, id)
	if err != nil {
		s.logger.ErrorContext(ctx, "purge group", "group_id", id, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The purge did not finish. Some entries may be left; try again.")
		return
	}
	s.audit(r, "group.purge", groupLabel(g), fmt.Sprintf("%d entries, %s", res.Entries, humanBytes(res.Bytes)))
	writeJSON(w, http.StatusOK, purgeJSON{Entries: res.Entries, Bytes: res.Bytes})
}

// handleDeleteGroup deletes one group. Groups are deleted one at a time, from
// their own page: each deletion cuts a set of devices off for good, so each
// is a decision about one group.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request, _ session) {
	g, err := s.groupByHandle(r.Context(), r.PathValue("handle"))
	var name string
	switch {
	case err == nil:
		name, err = s.deleteGroup(r, g.ID)
	case !errors.Is(err, store.ErrNotFound):
		s.logger.ErrorContext(r.Context(), "find group to delete", "error", err)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "Nothing was deleted: that group no longer exists.")
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, "The group could not be deleted. Check the server log.")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"name": name})
	}
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
	if s.storeSvc != nil {
		// The deletion queued the group's store; empty it now rather than at
		// the next sweep.
		s.storeSvc.Kick()
		if u, err := s.store.StoreUsage(ctx, id); err == nil && u.Items > 0 {
			detail += fmt.Sprintf(", %d store %s", u.Items, plural(int(u.Items), "item", "items"))
		}
	}
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
