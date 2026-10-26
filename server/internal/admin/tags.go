package admin

import (
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// tagJSON is one row of the tag screen.
type tagJSON struct {
	Name      string    `json:"name"`
	Groups    int       `json:"groups"`
	Devices   int       `json:"devices"`
	UsedBytes int64     `json:"used_bytes"`
	Limit     limitJSON `json:"limit"`

	// StoreLimit is the tag's store limit override (SPEC §4.6).
	StoreLimit limitJSON `json:"store_limit"`

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

	// HasStore says whether the relay has a store, and the store limits
	// the tag screen's explanations name.
	HasStore               bool  `json:"has_store"`
	DefaultStoreLimitBytes int64 `json:"default_store_limit_bytes"`
	TotalStoreLimitBytes   int64 `json:"total_store_limit_bytes"`

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

// handleTags returns one page of the tag screen: each tag with the groups,
// devices, storage and store under it.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request, _ session) {
	ctx := r.Context()
	tags, err := s.store.ListTags(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "list tags", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The tags could not be loaded.")
		return
	}
	probs := s.problems(ctx, "tags")
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		probs.add("the groups", err)
	}

	rows := make([]tagJSON, 0, len(tags))
	for _, t := range tags {
		rows = append(rows, tagJSON{
			Name:       t.Name,
			Limit:      limitJSON{Enabled: t.Limit.Enabled, Bytes: t.Limit.Bytes},
			StoreLimit: limitJSON{Enabled: t.StoreLimit.Enabled, Bytes: t.StoreLimit.Bytes},
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

	v := tagsJSON{
		tablePage:         tagTable.run(rows, r.URL.Query()),
		DefaultLimitBytes: s.dashboard.GroupLimit,
		TotalLimitBytes:   s.dashboard.TotalLimit,
		Problems:          probs.list,
	}
	if s.storeSvc != nil {
		cfg := s.storeSvc.Config()
		v.HasStore = true
		v.DefaultStoreLimitBytes = cfg.MaxGroupBytes
		v.TotalStoreLimitBytes = cfg.MaxTotalBytes
	}
	writeJSON(w, http.StatusOK, v)
}

// handleCreateTag adds a tag.
func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, ok := validTagName(req.Name)
	if !ok {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("A tag name starts with a letter or digit and has at most %d letters, digits, spaces, dots, dashes or underscores.", maxTagNameLen))
		return
	}
	_, err := s.store.CreateTag(r.Context(), name)
	switch {
	case errors.Is(err, store.ErrTagExists):
		writeJSONError(w, http.StatusConflict, "There is already a tag called “"+name+"”.")
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "create tag", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The tag could not be created.")
		return
	}
	s.audit(r, "tag.create", name, "")
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// handleSetTagLimit stores a tag's storage limit override and applies it to
// every group under the tag.
func (s *Server) handleSetTagLimit(w http.ResponseWriter, r *http.Request, _ session) {
	var req limitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	name := r.PathValue("name")
	t, err := s.store.GetTag(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "That tag no longer exists.")
		return
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "read tag", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The tag could not be read.")
		return
	}
	limit, msg := s.parseLimit(req, t.Limit)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.store.SetTagLimit(ctx, name, limit); err != nil {
		s.logger.ErrorContext(ctx, "set tag storage limit", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The limit could not be saved.")
		return
	}
	if err := s.SyncStorageLimits(ctx); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limits", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The limit was saved but could not be applied yet. It will be at the next restart.")
		return
	}
	s.audit(r, "tag.limit", name, limitAuditText(limit))
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteTag removes a tag. Its groups are left untagged and fall back
// to their own limit or the default.
func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request, _ session) {
	ctx := r.Context()
	name := r.PathValue("name")
	err := s.store.DeleteTag(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "That tag no longer exists.")
		return
	case err != nil:
		s.logger.ErrorContext(ctx, "delete tag", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The tag could not be deleted.")
		return
	}
	if err := s.SyncStorageLimits(ctx); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limits", "error", err)
	}
	s.audit(r, "tag.delete", name, "")
	w.WriteHeader(http.StatusNoContent)
}
