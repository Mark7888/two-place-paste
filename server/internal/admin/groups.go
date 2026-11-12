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

// limitChange is a change to a limit override: the checkbox, and the size
// when one was given. A nil size keeps the stored bytes, so unticking the box
// does not demand a value.
type limitChange struct {
	Enabled bool
	Bytes   *int64
}

// sizeChange reads a limit change as the admin page sends it: the checkbox
// and the size as typed. example is how the error message suggests a size.
func sizeChange(req limitRequest, example string) (limitChange, error) {
	c := limitChange{Enabled: req.Enabled}
	if raw := strings.TrimSpace(req.Size); raw != "" {
		n, err := config.ParseByteSize(raw)
		if err != nil || n < 0 {
			return c, invalid("That is not a size. Use e.g. " + example + " or 0 for no limit of its own.")
		}
		c.Bytes = &n
	}
	return c, nil
}

// checkLimit applies a storage limit change to the current override, and
// checks it against the total limit.
func (s *Server) checkLimit(c limitChange, current store.LimitOverride) (store.LimitOverride, error) {
	return checkOverride(c, current, s.dashboard.TotalLimit, "The limit cannot be above the server's total limit of %s.")
}

// checkStoreLimit is checkLimit for a store limit, checked against the
// relay's total store limit instead of the blob one.
func (s *Server) checkStoreLimit(c limitChange, current store.LimitOverride) (store.LimitOverride, error) {
	return checkOverride(c, current, s.storeSvc.Config().MaxTotalBytes, "The limit cannot be above the server's total store limit of %s.")
}

func checkOverride(c limitChange, current store.LimitOverride, total int64, tooBig string) (store.LimitOverride, error) {
	out := store.LimitOverride{Bytes: current.Bytes, Enabled: c.Enabled}
	if c.Bytes != nil {
		if *c.Bytes < 0 {
			return current, invalid("A size cannot be negative.")
		}
		out.Bytes = *c.Bytes
	}
	if out.Enabled && total > 0 && out.Bytes > total {
		return current, invalid(fmt.Sprintf(tooBig, humanBytes(total)))
	}
	return out, nil
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

// groupDetailView builds one group's page.
func (s *Server) groupDetailView(ctx context.Context, g store.Group) groupDetailJSON {
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
	return v
}

// handleGroup returns one group's page.
func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request, _ session) {
	g, err := s.findGroup(r.Context(), r.PathValue("handle"))
	if err != nil {
		writeOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.groupDetailView(r.Context(), g))
}

// findGroup finds the group a handle names, as an operation.
func (s *Server) findGroup(ctx context.Context, handle string) (store.Group, error) {
	g, err := s.groupByHandle(ctx, handle)
	if err != nil {
		return store.Group{}, s.groupErr(ctx, "", err, "read group")
	}
	return g, nil
}

// groupErr turns a store error from reading or editing a group into an
// operation error.
func (s *Server) groupErr(ctx context.Context, id string, err error, what string) error {
	if errors.Is(err, store.ErrNotFound) {
		return notFound("That group does not exist. It may have been deleted.")
	}
	s.logger.ErrorContext(ctx, what, "group_id", id, "error", err)
	return failed("The change could not be saved.")
}

// groupChange is a change to a group. A nil field is left as it is.
type groupChange struct {
	Name       *string
	Tag        *string
	Limit      *limitChange
	Store      *bool
	StoreLimit *limitChange
}

// updateGroup checks every field of a change, then applies them one by one.
// A change with one bad field changes nothing. Each applied field is its own
// audit entry, as it is when the admin page changes one at a time.
func (s *Server) updateGroup(ctx context.Context, g store.Group, c groupChange) error {
	var (
		name       string
		limit      store.LimitOverride
		storeLimit store.LimitOverride
		err        error
	)
	if c.Name != nil {
		var ok bool
		if name, ok = validName(*c.Name, maxGroupNameLen); !ok {
			return invalid(fmt.Sprintf("Give the group a name of at most %d characters.", maxGroupNameLen))
		}
	}
	if c.Tag != nil && *c.Tag != "" {
		if _, err := s.store.GetTag(ctx, *c.Tag); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return invalid("That tag no longer exists.")
			}
			s.logger.ErrorContext(ctx, "read tag", "error", err)
			return failed("The tag could not be read.")
		}
	}
	if c.Limit != nil {
		if limit, err = s.checkLimit(*c.Limit, g.Limit); err != nil {
			return err
		}
	}
	if (c.Store != nil || c.StoreLimit != nil) && s.storeSvc == nil {
		return unavailable("This server has no store.")
	}
	if c.StoreLimit != nil {
		if storeLimit, err = s.checkStoreLimit(*c.StoreLimit, g.StoreLimit); err != nil {
			return err
		}
	}

	if c.Name != nil {
		if err := s.renameGroup(ctx, g, name); err != nil {
			return err
		}
	}
	if c.Tag != nil {
		if err := s.setGroupTag(ctx, g, *c.Tag); err != nil {
			return err
		}
	}
	if c.Limit != nil {
		if err := s.setGroupLimit(ctx, g, limit); err != nil {
			return err
		}
	}
	if c.Store != nil {
		if err := s.setGroupStore(ctx, g, *c.Store); err != nil {
			return err
		}
	}
	if c.StoreLimit != nil {
		if err := s.setGroupStoreLimit(ctx, g, storeLimit); err != nil {
			return err
		}
	}
	return nil
}

// renameGroup sets a group's display name and tells its devices.
func (s *Server) renameGroup(ctx context.Context, g store.Group, name string) error {
	if err := s.store.RenameGroup(ctx, g.ID, name); err != nil {
		return s.groupErr(ctx, g.ID, err, "rename group")
	}
	if s.live != nil {
		s.live.AnnounceRename(ctx, g.ID, name)
	}
	s.auditCtx(ctx, "group.rename", groupLabel(g), "to "+name)
	return nil
}

// setGroupTag puts a group under a tag, or under none.
func (s *Server) setGroupTag(ctx context.Context, g store.Group, tag string) error {
	err := s.store.SetGroupTag(ctx, g.ID, tag)
	if errors.Is(err, store.ErrNotFound) {
		// The group was found a moment ago, so it is the tag that is gone.
		return invalid("That tag no longer exists.")
	}
	if err != nil {
		return s.groupErr(ctx, g.ID, err, "set group tag")
	}
	if err := s.applyGroupLimit(ctx, g.ID); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limit", "group_id", g.ID, "error", err)
	}
	detail := "untagged"
	if tag != "" {
		detail = "tagged " + tag
	}
	s.auditCtx(ctx, "group.tag", groupLabel(g), detail)
	return nil
}

// setGroupLimit stores a group's own storage limit override and applies
// whatever limit now wins.
func (s *Server) setGroupLimit(ctx context.Context, g store.Group, limit store.LimitOverride) error {
	if err := s.store.SetGroupLimit(ctx, g.ID, limit); err != nil {
		return s.groupErr(ctx, g.ID, err, "set group storage limit")
	}
	if err := s.applyGroupLimit(ctx, g.ID); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limit", "group_id", g.ID, "error", err)
		return failed("The limit was saved but could not be applied yet. It will be at the next restart.")
	}
	s.auditCtx(ctx, "group.limit", groupLabel(g), limitAuditText(limit))
	return nil
}

// handleRenameGroup sets a group's display name.
func (s *Server) handleRenameGroup(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	s.changeGroup(w, r, groupChange{Name: &req.Name})
}

// handleSetGroupTag puts a group under a tag, or under none.
func (s *Server) handleSetGroupTag(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Tag string `json:"tag"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	s.changeGroup(w, r, groupChange{Tag: &req.Tag})
}

// handleSetGroupLimit stores a group's own storage limit override.
func (s *Server) handleSetGroupLimit(w http.ResponseWriter, r *http.Request, _ session) {
	var req limitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	c, err := sizeChange(req, "“5 GiB”, “500 MB”")
	if err != nil {
		writeOpError(w, err)
		return
	}
	s.changeGroup(w, r, groupChange{Limit: &c})
}

// changeGroup applies one change from the admin page to the group the path
// names.
func (s *Server) changeGroup(w http.ResponseWriter, r *http.Request, c groupChange) {
	ctx := r.Context()
	g, err := s.findGroup(ctx, r.PathValue("handle"))
	if err == nil {
		err = s.updateGroup(ctx, g, c)
	}
	if err != nil {
		writeOpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func limitAuditText(l store.LimitOverride) string {
	if !l.Enabled {
		return "own limit off (kept " + limitText(l.Bytes) + ")"
	}
	return "own limit " + limitText(l.Bytes)
}

// purgeJSON is what a purge freed.
type purgeJSON struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
}

// purgeGroup deletes a group's entries now.
func (s *Server) purgeGroup(ctx context.Context, handle string) (purgeJSON, error) {
	if s.purger == nil {
		return purgeJSON{}, unavailable("Purging is not available on this server.")
	}
	g, err := s.findGroup(ctx, handle)
	if err != nil {
		return purgeJSON{}, err
	}
	res, err := s.purger.PurgeGroup(ctx, g.ID)
	if err != nil {
		s.logger.ErrorContext(ctx, "purge group", "group_id", g.ID, "error", err)
		return purgeJSON{}, failed("The purge did not finish. Some entries may be left; try again.")
	}
	s.auditCtx(ctx, "group.purge", groupLabel(g), fmt.Sprintf("%d entries, %s", res.Entries, humanBytes(res.Bytes)))
	return purgeJSON{Entries: res.Entries, Bytes: res.Bytes}, nil
}

// handlePurgeGroup deletes a group's entries now.
func (s *Server) handlePurgeGroup(w http.ResponseWriter, r *http.Request, _ session) {
	res, err := s.purgeGroup(r.Context(), r.PathValue("handle"))
	if err != nil {
		writeOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// deleteGroupByHandle deletes the group a handle names and returns its name.
// Groups are deleted one at a time: each deletion cuts a set of devices off
// for good, so each is a decision about one group.
func (s *Server) deleteGroupByHandle(ctx context.Context, handle string) (string, error) {
	g, err := s.groupByHandle(ctx, handle)
	var name string
	switch {
	case err == nil:
		name, err = s.deleteGroup(ctx, g.ID)
	case !errors.Is(err, store.ErrNotFound):
		s.logger.ErrorContext(ctx, "find group to delete", "error", err)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "", notFound("Nothing was deleted: that group no longer exists.")
	case err != nil:
		return "", failed("The group could not be deleted. Check the server log.")
	}
	return name, nil
}

// handleDeleteGroup deletes one group, from its own page.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request, _ session) {
	name, err := s.deleteGroupByHandle(r.Context(), r.PathValue("handle"))
	if err != nil {
		writeOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// deleteGroup removes a group: the persistent zone first, which is what cuts
// its devices off, then their sockets, then its entries
// (docs/internal/plans/admin-group-management.md D6). A purge failure after
// the group is gone leaves only entries nobody can read, which expire within
// 24 hours anyway.
//
// It returns the deleted group's name.
func (s *Server) deleteGroup(ctx context.Context, id string) (string, error) {
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
			detail += fmt.Sprintf(", %d store %s", u.Items, plural(u.Items, "item", "items"))
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
	s.auditCtx(ctx, "group.delete", groupLabel(del.Group), detail)
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

// plural picks the word for a count. It takes any integer type, so a count
// read as int64 is never narrowed to int just to choose a word.
func plural[N ~int | ~int64](n N, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
