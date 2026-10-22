package admin

import (
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// tagJSON is one row of the tag screen.
type tagJSON struct {
	Name      string    `json:"name"`
	Groups    int       `json:"groups"`
	Devices   int       `json:"devices"`
	UsedBytes int64     `json:"used_bytes"`
	Limit     limitJSON `json:"limit"`
}

// tagsJSON is the tag screen.
type tagsJSON struct {
	Tags              []tagJSON `json:"tags"`
	DefaultLimitBytes int64     `json:"default_limit_bytes"`
	TotalLimitBytes   int64     `json:"total_limit_bytes"`
	Problems          []string  `json:"problems"`
}

// handleTags returns the tag screen: every tag with the groups, devices and
// storage under it, by name.
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

	v := tagsJSON{
		Tags:              make([]tagJSON, 0, len(tags)),
		DefaultLimitBytes: s.dashboard.GroupLimit,
		TotalLimitBytes:   s.dashboard.TotalLimit,
	}
	for _, t := range tags {
		v.Tags = append(v.Tags, tagJSON{Name: t.Name, Limit: limitJSON{Enabled: t.Limit.Enabled, Bytes: t.Limit.Bytes}})
	}
	byName := make(map[string]*tagJSON, len(v.Tags))
	for i := range v.Tags {
		byName[v.Tags[i].Name] = &v.Tags[i]
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
	}
	sort.Slice(v.Tags, func(i, j int) bool { return v.Tags[i].Name < v.Tags[j].Name })
	v.Problems = probs.list
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
