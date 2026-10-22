package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// tagView is one row of the tag screen.
type tagView struct {
	Name       string
	Path       string
	Groups     int
	Devices    int
	Used       string
	LimitOn    bool
	LimitInput string
	LimitText  string
	Filter     string
}

// tagsView is the tag screen.
type tagsView struct {
	Tags        []tagView
	DefaultText string
	TotalText   string
}

func tagPath(name string) string { return "/admin/tags/" + url.PathEscape(name) }

// handleTags renders the tag screen.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/admin/login")
		return
	}
	s.renderTags(w, r, sess, http.StatusOK, "")
}

func (s *Server) renderTags(w http.ResponseWriter, r *http.Request, sess session, status int, msg string) {
	ctx := r.Context()
	data := s.page(sess, "Tags", "tags")
	data.Error = msg

	tags, err := s.store.ListTags(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "list tags", "error", err)
		data.Error = "The tags could not be loaded."
		s.render(w, r, http.StatusInternalServerError, "tags.html", data)
		return
	}
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "list groups", "error", err)
		data.Problems = append(data.Problems, "Could not read the groups.")
	}

	v := &tagsView{DefaultText: limitText(s.dashboard.GroupLimit), TotalText: limitText(s.dashboard.TotalLimit)}
	byName := make(map[string]*tagView, len(tags))
	for _, t := range tags {
		v.Tags = append(v.Tags, tagView{
			Name:       t.Name,
			Path:       tagPath(t.Name),
			LimitOn:    t.Limit.Enabled,
			LimitInput: sizeInput(t.Limit.Bytes),
			LimitText:  limitText(t.Limit.Bytes),
			Filter:     "/admin/groups?tag=" + url.QueryEscape(t.Name),
		})
	}
	for i := range v.Tags {
		byName[v.Tags[i].Name] = &v.Tags[i]
	}
	used := make(map[string]int64)
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
				used[g.Tag] += u.Bytes
			}
		}
	}
	for i := range v.Tags {
		v.Tags[i].Used = humanBytes(used[v.Tags[i].Name])
	}
	sort.Slice(v.Tags, func(i, j int) bool { return v.Tags[i].Name < v.Tags[j].Name })

	data.TagsPage = v
	s.render(w, r, status, "tags.html", data)
}

// handleCreateTag adds a tag.
func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	name, ok := validTagName(r.PostFormValue("name"))
	if !ok {
		s.renderTags(w, r, sess, http.StatusBadRequest,
			fmt.Sprintf("A tag name starts with a letter or digit and has at most %d letters, digits, spaces, dots, dashes or underscores.", maxTagNameLen))
		return
	}
	_, err := s.store.CreateTag(r.Context(), name)
	switch {
	case errors.Is(err, store.ErrTagExists):
		s.renderTags(w, r, sess, http.StatusConflict, "There is already a tag called “"+name+"”.")
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "create tag", "error", err)
		s.renderTags(w, r, sess, http.StatusInternalServerError, "The tag could not be created.")
		return
	}
	s.audit(r, "tag.create", name, "")
	s.sessions.setNotice(sess.id, "Tag “"+name+"” created. Put groups under it from each group's page.")
	s.redirect(w, r, "/admin/tags")
}

// handleSetTagLimit stores a tag's storage limit override and applies it to
// every group under the tag.
func (s *Server) handleSetTagLimit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := r.PathValue("name")
	t, err := s.store.GetTag(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		s.renderTags(w, r, sess, http.StatusNotFound, "That tag no longer exists.")
		return
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "read tag", "error", err)
		s.renderTags(w, r, sess, http.StatusInternalServerError, "The tag could not be read.")
		return
	}
	limit, msg := s.parseLimitForm(r, t.Limit)
	if msg != "" {
		s.renderTags(w, r, sess, http.StatusBadRequest, msg)
		return
	}
	if err := s.store.SetTagLimit(ctx, name, limit); err != nil {
		s.logger.ErrorContext(ctx, "set tag storage limit", "error", err)
		s.renderTags(w, r, sess, http.StatusInternalServerError, "The limit could not be saved.")
		return
	}
	if err := s.SyncStorageLimits(ctx); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limits", "error", err)
		s.renderTags(w, r, sess, http.StatusInternalServerError, "The limit was saved but could not be applied yet. It will be at the next restart.")
		return
	}
	s.audit(r, "tag.limit", name, limitAuditText(limit))
	s.sessions.setNotice(sess.id, "Storage limit of “"+name+"” saved.")
	s.redirect(w, r, "/admin/tags")
}

// handleDeleteTag removes a tag. Its groups are left untagged and fall back
// to their own limit or the default.
func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.formSession(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := r.PathValue("name")
	err := s.store.DeleteTag(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.renderTags(w, r, sess, http.StatusNotFound, "That tag no longer exists.")
		return
	case err != nil:
		s.logger.ErrorContext(ctx, "delete tag", "error", err)
		s.renderTags(w, r, sess, http.StatusInternalServerError, "The tag could not be deleted.")
		return
	}
	if err := s.SyncStorageLimits(ctx); err != nil {
		s.logger.ErrorContext(ctx, "apply storage limits", "error", err)
	}
	s.audit(r, "tag.delete", name, "")
	s.sessions.setNotice(sess.id, "Tag “"+name+"” deleted. Its groups are untagged now.")
	s.redirect(w, r, "/admin/tags")
}
