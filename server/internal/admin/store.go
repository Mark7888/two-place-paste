package admin

import (
	"context"
	"fmt"
	"net/http"

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
	Enabled     bool   `json:"enabled"`
	Emptying    bool   `json:"emptying"`
	UsedBytes   int64  `json:"used_bytes"`
	UsedItems   int64  `json:"used_items"`
	LimitBytes  int64  `json:"limit_bytes"`
	LimitSource string `json:"limit_source"`
}

// storeRow builds a group's store cell for the groups list, or nil when the
// relay has no store.
func (s *Server) storeRow(ctx context.Context, g store.Group, tags map[string]store.Tag, probs *problems) *storeRowJSON {
	if s.storeSvc == nil {
		return nil
	}
	limit := s.storeSvc.GroupLimit(g, tagOf(g, tags))
	v := &storeRowJSON{
		Enabled:     g.StoreEnabled,
		LimitBytes:  storeShown(limit.Bytes, s.storeSvc.Config().MaxTotalBytes),
		LimitSource: limit.Source,
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

// setGroupStore turns a group's store on or off, and tells its online
// devices. Turning it off keeps every item.
func (s *Server) setGroupStore(ctx context.Context, g store.Group, enabled bool) error {
	if s.storeSvc == nil {
		return unavailable("This server has no store.")
	}
	if err := s.store.SetGroupStore(ctx, g.ID, enabled); err != nil {
		return s.groupErr(ctx, g.ID, err, "set group store")
	}
	// A store being emptied stays closed; its devices hear when it opens.
	if emptying, _ := s.storeSvc.Reclaiming(ctx, g.ID); !emptying {
		if a, ok := s.live.(storeAnnouncer); ok {
			a.AnnounceStoreState(ctx, g.ID, enabled)
		}
	}
	detail := "turned on"
	if !enabled {
		detail = "turned off"
		if u, err := s.store.StoreUsage(ctx, g.ID); err == nil {
			detail += fmt.Sprintf(", %d %s kept (%s)", u.Items, plural(u.Items, "item", "items"), humanBytes(u.Bytes))
		}
	}
	s.auditCtx(ctx, "group.store", groupLabel(g), detail)
	return nil
}

// setGroupStoreLimit stores a group's own store limit override.
func (s *Server) setGroupStoreLimit(ctx context.Context, g store.Group, limit store.LimitOverride) error {
	if err := s.store.SetGroupStoreLimit(ctx, g.ID, limit); err != nil {
		return s.groupErr(ctx, g.ID, err, "set group store limit")
	}
	s.auditCtx(ctx, "group.storelimit", groupLabel(g), limitAuditText(limit))
	return nil
}

// handleSetGroupStore turns a group's store on or off.
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
	s.changeGroup(w, r, groupChange{Store: &req.Enabled})
}

// storeSizeExample is how a store limit's error message suggests a size.
const storeSizeExample = "“100 MiB”, “1 GB”"

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
	c, err := sizeChange(req, storeSizeExample)
	if err != nil {
		writeOpError(w, err)
		return
	}
	s.changeGroup(w, r, groupChange{StoreLimit: &c})
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
	c, err := sizeChange(req, storeSizeExample)
	if err == nil {
		err = s.updateTag(r.Context(), r.PathValue("name"), tagChange{StoreLimit: &c})
	}
	if err != nil {
		writeOpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// purgeGroupStore deletes every item of a group's store, for every device,
// and returns what the store held. It only queues the work: the store closes
// at once and is emptied in the background, so a store of any size answers
// immediately. The switch and the limits stay as they are.
func (s *Server) purgeGroupStore(ctx context.Context, handle string) (store.ItemUsage, error) {
	if s.storeSvc == nil {
		return store.ItemUsage{}, unavailable("This server has no store.")
	}
	g, err := s.findGroup(ctx, handle)
	if err != nil {
		return store.ItemUsage{}, err
	}
	before, err := s.store.StoreUsage(ctx, g.ID)
	if err == nil {
		err = s.storeSvc.Purge(ctx, g.ID)
	}
	if err != nil {
		return store.ItemUsage{}, s.groupErr(ctx, g.ID, err, "purge group store")
	}
	s.auditCtx(ctx, "group.storepurge", groupLabel(g),
		fmt.Sprintf("%d %s (%s)", before.Items, plural(before.Items, "item", "items"), humanBytes(before.Bytes)))
	return before, nil
}

// handlePurgeGroupStore empties a group's store.
func (s *Server) handlePurgeGroupStore(w http.ResponseWriter, r *http.Request, _ session) {
	if _, err := s.purgeGroupStore(r.Context(), r.PathValue("handle")); err != nil {
		writeOpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
