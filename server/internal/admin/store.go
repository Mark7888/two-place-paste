package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Mark7888/two-place-paste/server/internal/config"
	"github.com/Mark7888/two-place-paste/server/internal/items"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// The store on the admin screens (SPEC §4.4, §4.6): the switch that turns a
// group's store on or off, its usage against its own limits, and the limit
// overrides on a group and a tag. Like every screen it shows sizes and
// counts, never an item's name or content — the relay has neither to show.

// StoreService is the store, as far as the admin screens need it.
type StoreService interface {
	// GroupLimit resolves a group's store limit and where it comes from.
	GroupLimit(g store.Group, tag *store.Tag) items.Limit

	// Config is the configured store limits.
	Config() items.Config

	// Purge queues a group's store to be emptied in the background, and
	// Reclaiming reports whether it still is.
	Purge(ctx context.Context, groupID string) error
	Reclaiming(ctx context.Context, groupID string) (bool, error)

	// Kick wakes the background reclaim, after a group was deleted.
	Kick()
}

// storeAnnouncer is implemented by a transport that can tell a group's
// devices its store was turned on or off. It is optional: the screens work
// without it, and a device then learns at its next StoreLimitsRequest.
type storeAnnouncer interface {
	AnnounceStoreState(ctx context.Context, groupID string, enabled bool)
}

// groupStoreJSON is the store section of the group page.
type groupStoreJSON struct {
	Enabled bool `json:"enabled"`

	// Emptying is true while a purge or the group's deletion is emptying the
	// store in the background; it is closed until then.
	Emptying bool `json:"emptying"`

	UsedBytes int64 `json:"used_bytes"`
	UsedItems int64 `json:"used_items"`

	// LimitBytes is the cap the group runs into — its limit, or the relay's
	// total store limit when that is lower — and LimitSource where its own
	// limit comes from. Zero is unlimited.
	LimitBytes  int64  `json:"limit_bytes"`
	LimitSource string `json:"limit_source"`

	OwnLimit          limitJSON `json:"own_limit"`
	TagLimitBytes     *int64    `json:"tag_limit_bytes"`
	DefaultLimitBytes int64     `json:"default_limit_bytes"`

	MaxItems     int64 `json:"max_items"`
	MaxItemBytes int64 `json:"max_item_bytes"`
}

// storeRowJSON is a group's store on the groups list: whether it is on, and
// how much it holds against the cap it runs into.
type storeRowJSON struct {
	Enabled    bool  `json:"enabled"`
	Emptying   bool  `json:"emptying"`
	UsedBytes  int64 `json:"used_bytes"`
	UsedItems  int64 `json:"used_items"`
	LimitBytes int64 `json:"limit_bytes"`
}

// storeRow builds a group's store cell for the groups list, or nil when the
// relay has no store.
func (s *Server) storeRow(ctx context.Context, g store.Group, tags map[string]store.Tag, probs *problems) *storeRowJSON {
	if s.storeSvc == nil {
		return nil
	}
	limit := s.storeSvc.GroupLimit(g, tagOf(g, tags))
	v := &storeRowJSON{
		Enabled:    g.StoreEnabled,
		LimitBytes: storeShown(limit.Bytes, s.storeSvc.Config().MaxTotalBytes),
	}
	if u, err := s.store.StoreUsage(ctx, g.ID); err != nil {
		probs.add("the store usage of "+g.Name, err)
	} else {
		v.UsedBytes, v.UsedItems = u.Bytes, u.Items
	}
	if emptying, err := s.storeSvc.Reclaiming(ctx, g.ID); err != nil {
		probs.add("the store state of "+g.Name, err)
	} else {
		v.Emptying = emptying
	}
	return v
}

// tagOf is the tag a group is under, or nil when it has none or its tag is
// gone.
func tagOf(g store.Group, tags map[string]store.Tag) *store.Tag {
	if t, ok := tags[g.Tag]; ok && g.Tag != "" {
		return &t
	}
	return nil
}

// groupStore builds the store section of a group's page, or nil when the
// relay has no store.
func (s *Server) groupStore(ctx context.Context, g store.Group, tags map[string]store.Tag, probs *problems) *groupStoreJSON {
	if s.storeSvc == nil {
		return nil
	}
	cfg := s.storeSvc.Config()
	tag := tagOf(g, tags)
	limit := s.storeSvc.GroupLimit(g, tag)
	v := &groupStoreJSON{
		Enabled:           g.StoreEnabled,
		LimitBytes:        storeShown(limit.Bytes, cfg.MaxTotalBytes),
		LimitSource:       limit.Source,
		OwnLimit:          limitJSON{Enabled: g.StoreLimit.Enabled, Bytes: g.StoreLimit.Bytes},
		DefaultLimitBytes: cfg.MaxGroupBytes,
		MaxItems:          int64(cfg.MaxGroupItems),
		MaxItemBytes:      cfg.MaxItemBytes,
	}
	if tag != nil && tag.StoreLimit.Enabled {
		b := tag.StoreLimit.Bytes
		v.TagLimitBytes = &b
	}
	if u, err := s.store.StoreUsage(ctx, g.ID); err != nil {
		probs.add("the store usage", err)
	} else {
		v.UsedBytes, v.UsedItems = u.Bytes, u.Items
	}
	if emptying, err := s.storeSvc.Reclaiming(ctx, g.ID); err != nil {
		probs.add("the store state", err)
	} else {
		v.Emptying = emptying
	}
	return v
}

// storeShown is the cap a group's store runs into: its own limit, or the
// total when that is lower or the group has none.
func storeShown(group, total int64) int64 {
	if total > 0 && (group == 0 || total < group) {
		return total
	}
	return group
}

// handleSetGroupStore turns a group's store on or off, and tells its online
// devices. Turning it off keeps every item.
func (s *Server) handleSetGroupStore(w http.ResponseWriter, r *http.Request, _ session) {
	if s.storeSvc == nil {
		writeJSONError(w, http.StatusNotImplemented, "This server has no store.")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	g, err := s.groupByHandle(ctx, r.PathValue("handle"))
	id := g.ID
	if err == nil {
		err = s.store.SetGroupStore(ctx, id, req.Enabled)
	}
	if !s.groupFound(w, r, id, err, "set group store") {
		return
	}
	// A store being emptied stays closed; its devices hear when it opens.
	if emptying, _ := s.storeSvc.Reclaiming(ctx, id); !emptying {
		if a, ok := s.live.(storeAnnouncer); ok {
			a.AnnounceStoreState(ctx, id, req.Enabled)
		}
	}
	detail := "turned on"
	if !req.Enabled {
		detail = "turned off"
		if u, err := s.store.StoreUsage(ctx, id); err == nil {
			detail += fmt.Sprintf(", %d %s kept (%s)", u.Items, plural(u.Items, "item", "items"), humanBytes(u.Bytes))
		}
	}
	s.audit(r, "group.store", groupLabel(g), detail)
	w.WriteHeader(http.StatusNoContent)
}

// handleSetGroupStoreLimit stores a group's own store limit override.
func (s *Server) handleSetGroupStoreLimit(w http.ResponseWriter, r *http.Request, _ session) {
	if s.storeSvc == nil {
		writeJSONError(w, http.StatusNotImplemented, "This server has no store.")
		return
	}
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
	limit, msg := s.parseStoreLimit(req, g.StoreLimit)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}
	if !s.groupFound(w, r, id, s.store.SetGroupStoreLimit(ctx, id, limit), "set group store limit") {
		return
	}
	s.audit(r, "group.storelimit", groupLabel(g), limitAuditText(limit))
	w.WriteHeader(http.StatusNoContent)
}

// handleSetTagStoreLimit stores a tag's store limit override.
func (s *Server) handleSetTagStoreLimit(w http.ResponseWriter, r *http.Request, _ session) {
	if s.storeSvc == nil {
		writeJSONError(w, http.StatusNotImplemented, "This server has no store.")
		return
	}
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
	limit, msg := s.parseStoreLimit(req, t.StoreLimit)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.store.SetTagStoreLimit(ctx, name, limit); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "That tag no longer exists.")
			return
		}
		s.logger.ErrorContext(ctx, "set tag store limit", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The limit could not be saved.")
		return
	}
	s.audit(r, "tag.storelimit", name, limitAuditText(limit))
	w.WriteHeader(http.StatusNoContent)
}

// parseStoreLimit is parseLimit for a store limit, checked against the
// relay's total store limit instead of the blob one.
func (s *Server) parseStoreLimit(req limitRequest, current store.LimitOverride) (store.LimitOverride, string) {
	out := store.LimitOverride{Bytes: current.Bytes, Enabled: req.Enabled}
	if raw := req.Size; raw != "" {
		n, err := config.ParseByteSize(raw)
		if err != nil || n < 0 {
			return current, "That is not a size. Use e.g. “100 MiB”, “1 GB” or 0 for no limit of its own."
		}
		out.Bytes = n
	}
	if total := s.storeSvc.Config().MaxTotalBytes; out.Enabled && total > 0 && out.Bytes > total {
		return current, fmt.Sprintf("The limit cannot be above the server's total store limit of %s.", humanBytes(total))
	}
	return out, ""
}

// storeTotalsJSON is the store card of the dashboard.
type storeTotalsJSON struct {
	UsedBytes int64 `json:"used_bytes"`
	Items     int64 `json:"items"`

	// GroupsOn is how many groups have their store turned on.
	GroupsOn int `json:"groups_on"`

	LimitBytes      int64 `json:"limit_bytes"`
	GroupLimitBytes int64 `json:"group_limit_bytes"`
	MaxItemBytes    int64 `json:"max_item_bytes"`
	MaxGroupItems   int64 `json:"max_group_items"`
}

// storeTotals builds the dashboard's store card, or nil when the relay has
// no store.
func (s *Server) storeTotals(ctx context.Context, groups []store.Group, probs *problems) *storeTotalsJSON {
	if s.storeSvc == nil {
		return nil
	}
	cfg := s.storeSvc.Config()
	v := &storeTotalsJSON{
		LimitBytes:      cfg.MaxTotalBytes,
		GroupLimitBytes: cfg.MaxGroupBytes,
		MaxItemBytes:    cfg.MaxItemBytes,
		MaxGroupItems:   int64(cfg.MaxGroupItems),
	}
	if u, err := s.store.TotalStoreUsage(ctx); err != nil {
		probs.add("the store usage", err)
	} else {
		v.UsedBytes, v.Items = u.Bytes, u.Items
	}
	for _, g := range groups {
		if g.StoreEnabled {
			v.GroupsOn++
		}
	}
	return v
}

// handlePurgeGroupStore deletes every item of a group's store, for every
// device. The request only queues the work: the store closes at once and is
// emptied in the background, so a store of any size answers immediately.
// The switch and the limits stay as they are.
func (s *Server) handlePurgeGroupStore(w http.ResponseWriter, r *http.Request, _ session) {
	if s.storeSvc == nil {
		writeJSONError(w, http.StatusNotImplemented, "This server has no store.")
		return
	}
	ctx := r.Context()
	g, err := s.groupByHandle(ctx, r.PathValue("handle"))
	id := g.ID
	var before store.ItemUsage
	if err == nil {
		before, err = s.store.StoreUsage(ctx, id)
	}
	if err == nil {
		err = s.storeSvc.Purge(ctx, id)
	}
	if !s.groupFound(w, r, id, err, "purge group store") {
		return
	}
	s.audit(r, "group.storepurge", groupLabel(g),
		fmt.Sprintf("%d %s (%s)", before.Items, plural(before.Items, "item", "items"), humanBytes(before.Bytes)))
	w.WriteHeader(http.StatusNoContent)
}
