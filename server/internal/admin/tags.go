package admin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// tagJSON is one row of the tag screen.
type tagJSON struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Groups    int       `json:"groups"`
	Devices   int       `json:"devices"`
	UsedBytes int64     `json:"used_bytes"`
	Limit     limitJSON `json:"limit"`

	// StoreLimit is the tag's store limit override (SPEC §4.6).
	StoreLimit limitJSON `json:"store_limit"`

	// LimitRaised and StoreLimitRaised are true when the tag's limit is on
	// and below one entry, or one store item, so its groups get that floor
	// instead.
	LimitRaised      bool `json:"limit_raised"`
	StoreLimitRaised bool `json:"store_limit_raised"`

	// StoreBytes is what the stores of the tag's groups hold together, and
	// StoreGroupsOn how many of those groups have their store on.
	StoreBytes    int64 `json:"store_bytes"`
	StoreGroupsOn int   `json:"store_groups_on"`
}

// tagsJSON is the tag screen: one page of the tags, and the limits its
// explanations name.
type tagsJSON struct {
	tablePage[tagJSON]
	DefaultLimitBytes int64 `json:"default_limit_bytes"`
	TotalLimitBytes   int64 `json:"total_limit_bytes"`

	// MinLimitBytes is the smallest limit a tag may set: one entry.
	MinLimitBytes int64 `json:"min_limit_bytes"`

	// HasStore says whether the relay has a store, and the store limits
	// the tag screen's explanations name. MinStoreLimitBytes is the
	// smallest store limit: one item of the largest size and its record.
	HasStore               bool  `json:"has_store"`
	DefaultStoreLimitBytes int64 `json:"default_store_limit_bytes"`
	TotalStoreLimitBytes   int64 `json:"total_store_limit_bytes"`
	MinStoreLimitBytes     int64 `json:"min_store_limit_bytes"`

	Problems []string `json:"problems"`
}

// limitOrder ranks a tag's limit for sorting: off (the default applies)
// lowest, then the sizes, then "on with 0", which is no per-group limit at all.
func limitOrder(l limitJSON) int64 {
	switch {
	case !l.Enabled:
		return -1
	case l.Bytes == 0:
		return math.MaxInt64
	default:
		return l.Bytes
	}
}

// tagTable is the tag list: searched by name, by name by default.
var tagTable = table[tagJSON]{
	sorts: map[string]sortKey[tagJSON]{
		"name":    {cmp: func(a, b tagJSON) int { return byText(a.Name, b.Name) }},
		"groups":  {cmp: func(a, b tagJSON) int { return byNumber(a.Groups, b.Groups) }, desc: true},
		"devices": {cmp: func(a, b tagJSON) int { return byNumber(a.Devices, b.Devices) }, desc: true},
		"storage": {cmp: func(a, b tagJSON) int { return byNumber(a.UsedBytes, b.UsedBytes) }, desc: true},
		"store":   {cmp: func(a, b tagJSON) int { return byNumber(a.StoreBytes, b.StoreBytes) }, desc: true},
		"limit":   {cmp: func(a, b tagJSON) int { return byNumber(limitOrder(a.Limit), limitOrder(b.Limit)) }, desc: true},
	},
	defaultSort: "name",
	fields:      func(t tagJSON) []string { return []string{t.Name} },
	tie:         func(a, b tagJSON) int { return byText(a.Name, b.Name) },
}

// tagRows builds every tag's row: the groups, devices, storage and store
// under it. Parts that cannot be read are added to probs.
func (s *Server) tagRows(ctx context.Context, probs *problems) ([]tagJSON, error) {
	tags, err := s.store.ListTags(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "list tags", "error", err)
		return nil, failed("The tags could not be loaded.")
	}
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		probs.add("the groups", err)
	}

	var storeFloor int64
	if s.storeSvc != nil {
		storeFloor = s.storeSvc.Config().MinLimitBytes()
	}
	rows := make([]tagJSON, 0, len(tags))
	for _, t := range tags {
		_, raised := raiseLimit(t.Limit.Bytes, s.minLimit())
		_, storeRaised := raiseLimit(t.StoreLimit.Bytes, storeFloor)
		rows = append(rows, tagJSON{
			Name:             t.Name,
			CreatedAt:        t.CreatedAt.UTC(),
			Limit:            limitJSON{Enabled: t.Limit.Enabled, Bytes: t.Limit.Bytes},
			StoreLimit:       limitJSON{Enabled: t.StoreLimit.Enabled, Bytes: t.StoreLimit.Bytes},
			LimitRaised:      t.Limit.Enabled && raised,
			StoreLimitRaised: t.StoreLimit.Enabled && storeRaised,
		})
	}
	byName := make(map[string]*tagJSON, len(rows))
	for i := range rows {
		byName[rows[i].Name] = &rows[i]
	}
	for _, g := range groups {
		tv := byName[g.Tag]
		if tv == nil {
			continue
		}
		tv.Groups++
		if devices, err := s.store.ListDevices(ctx, g.ID); err == nil {
			tv.Devices += len(devices)
		}
		if s.dashboard.Ledger != nil {
			if u, err := s.dashboard.Ledger.GroupUsage(ctx, g.ID); err == nil {
				tv.UsedBytes += u.Bytes
			}
		}
		if s.storeSvc != nil {
			if g.StoreEnabled {
				tv.StoreGroupsOn++
			}
			if u, err := s.store.StoreUsage(ctx, g.ID); err == nil {
				tv.StoreBytes += u.Bytes
			}
		}
	}
	return rows, nil
}

// tagListView builds one page of the tag screen.
func (s *Server) tagListView(ctx context.Context, query url.Values) (tagsJSON, error) {
	probs := s.problems(ctx, "tags")
	rows, err := s.tagRows(ctx, probs)
	if err != nil {
		return tagsJSON{}, err
	}
	v := tagsJSON{
		tablePage:         tagTable.run(rows, query),
		DefaultLimitBytes: s.dashboard.GroupLimit,
		TotalLimitBytes:   s.dashboard.TotalLimit,
		MinLimitBytes:     s.minLimit(),
		Problems:          probs.list,
	}
	if s.storeSvc != nil {
		cfg := s.storeSvc.Config()
		v.HasStore = true
		v.DefaultStoreLimitBytes = cfg.MaxGroupBytes
		v.TotalStoreLimitBytes = cfg.MaxTotalBytes
		v.MinStoreLimitBytes = cfg.MinLimitBytes()
	}
	return v, nil
}

// tagView builds one tag's row.
func (s *Server) tagView(ctx context.Context, name string) (tagJSON, error) {
	rows, err := s.tagRows(ctx, s.problems(ctx, "tag"))
	if err != nil {
		return tagJSON{}, err
	}
	for _, row := range rows {
		if row.Name == name {
			return row, nil
		}
	}
	return tagJSON{}, notFound("That tag no longer exists.")
}

// handleTags returns one page of the tag screen.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request, _ session) {
	v, err := s.tagListView(r.Context(), r.URL.Query())
	if err != nil {
		writeOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// createTag adds a tag and returns its name as stored.
func (s *Server) createTag(ctx context.Context, raw string) (string, error) {
	name, ok := validTagName(raw)
	if !ok {
		return "", invalid(fmt.Sprintf("A tag name starts with a letter or digit and has at most %d letters, digits, spaces, dots, dashes or underscores.", maxTagNameLen))
	}
	_, err := s.store.CreateTag(ctx, name)
	switch {
	case errors.Is(err, store.ErrTagExists):
		return "", conflict("There is already a tag called “" + name + "”.")
	case err != nil:
		s.logger.ErrorContext(ctx, "create tag", "error", err)
		return "", failed("The tag could not be created.")
	}
	s.auditCtx(ctx, "tag.create", name, "")
	return name, nil
}

// handleCreateTag adds a tag.
func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := s.createTag(r.Context(), req.Name)
	if err != nil {
		writeOpError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// tagChange is a change to a tag's limits. A nil field is left as it is.
type tagChange struct {
	Limit      *limitChange
	StoreLimit *limitChange
}

// updateTag checks a change to a tag's limits, then applies it: the storage
// limit to every group under the tag, the store limit as the store next
// resolves it.
func (s *Server) updateTag(ctx context.Context, name string, c tagChange) error {
	if c.StoreLimit != nil && s.storeSvc == nil {
		return unavailable("This server has no store.")
	}
	t, err := s.store.GetTag(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("That tag no longer exists.")
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "read tag", "error", err)
		return failed("The tag could not be read.")
	}
	var limit, storeLimit store.LimitOverride
	if c.Limit != nil {
		if limit, err = s.checkLimit(*c.Limit, t.Limit); err != nil {
			return err
		}
	}
	if c.StoreLimit != nil {
		if storeLimit, err = s.checkStoreLimit(*c.StoreLimit, t.StoreLimit); err != nil {
			return err
		}
	}

	if c.Limit != nil {
		if err := s.store.SetTagLimit(ctx, name, limit); err != nil {
			s.logger.ErrorContext(ctx, "set tag storage limit", "error", err)
			return failed("The limit could not be saved.")
		}
		if err := s.SyncStorageLimits(ctx); err != nil {
			s.logger.ErrorContext(ctx, "apply storage limits", "error", err)
			return failed("The limit was saved but could not be applied yet. It will be at the next restart.")
		}
		s.auditCtx(ctx, "tag.limit", name, limitAuditText(limit))
	}
	if c.StoreLimit != nil {
		if err := s.store.SetTagStoreLimit(ctx, name, storeLimit); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return notFound("That tag no longer exists.")
			}
			s.logger.ErrorContext(ctx, "set tag store limit", "error", err)
			return failed("The limit could not be saved.")
		}
		s.auditCtx(ctx, "tag.storelimit", name, limitAuditText(storeLimit))
	}
	return nil
}

// handleSetTagLimit stores a tag's storage limit override and applies it to
// every group under the tag.
func (s *Server) handleSetTagLimit(w http.ResponseWriter, r *http.Request, _ session) {
	var req limitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	c, err := sizeChange(req, "“5 GiB”, “500 MB”")
	if err == nil {
		err = s.updateTag(r.Context(), r.PathValue("name"), tagChange{Limit: &c})
	}
	if err != nil {
		writeOpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteTag removes a tag. Its groups are left untagged and fall back to
// their own limit or the default.
func (s *Server) deleteTag(ctx context.Context, name string) error {
	err := s.store.DeleteTag(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return notFound("That tag no longer exists.")
	case err != nil:
		s.logger.ErrorContext(ctx, "delete tag", "error", err)
		return failed("The tag could not be deleted.")
	}
	if err := s.SyncStorageLimits(ctx); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limits", "error", err)
	}
	s.auditCtx(ctx, "tag.delete", name, "")
	return nil
}

// handleDeleteTag removes a tag.
func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request, _ session) {
	if err := s.deleteTag(r.Context(), r.PathValue("name")); err != nil {
		writeOpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
