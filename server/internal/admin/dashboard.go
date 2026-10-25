package admin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// The views below are what the admin API returns. They are built from counts,
// sizes and names only: no entry id, content type or filename reaches them,
// because the server has none of those to give (SPEC §2.3, §4.4). Sizes are
// bytes and times are RFC 3339 UTC; the app formats both.

// dashboardJSON is the dashboard: summary counts, storage and health.
type dashboardJSON struct {
	GroupCount     int `json:"group_count"`
	DeviceCount    int `json:"device_count"`
	UnusedTokens   int `json:"unused_tokens"`
	StaleCount     int `json:"stale_count"`
	StaleAfterDays int `json:"stale_after_days"`

	// Storage is nil when no ledger is wired.
	Storage *storageJSON `json:"storage"`

	// Store is nil when the relay has no store.
	Store *storeTotalsJSON `json:"store"`

	Health *healthJSON `json:"health"`

	// CanSweep shows the "Sweep now" button.
	CanSweep bool `json:"can_sweep"`

	// Problems lists the parts that could not be read. The rest of the page
	// still renders: a dashboard that fails whole when one Redis call does
	// is useless exactly when it is needed.
	Problems []string `json:"problems"`
}

type storageJSON struct {
	Backend  string `json:"backend"`
	Location string `json:"location"`

	UsedBytes  int64 `json:"used_bytes"`
	LimitBytes int64 `json:"limit_bytes"`
	Entries    int64 `json:"entries"`

	GroupLimitBytes int64 `json:"group_limit_bytes"`
	EntryMaxBytes   int64 `json:"entry_max_bytes"`
	InlineMaxBytes  int64 `json:"inline_max_bytes"`

	Evictions    int64 `json:"evictions"`
	EvictedBytes int64 `json:"evicted_bytes"`

	// Uploads is nil when the upload tracker is not wired.
	Uploads *uploadsJSON `json:"uploads"`

	// LastSweep is nil until the first sweep has run.
	LastSweep *sweepJSON `json:"last_sweep"`
}

type uploadsJSON struct {
	Count         int   `json:"count"`
	ReservedBytes int64 `json:"reserved_bytes"`
}

type sweepJSON struct {
	At     time.Time `json:"at"`
	TookMS int64     `json:"took_ms"`
	Error  string    `json:"error,omitempty"`
}

// groupJSON is one group's row: devices, activity, entries and storage.
type groupJSON struct {
	// Handle names the group in URLs and requests; its id is the token that
	// created it, and never leaves the server (handle.go).
	Handle    string    `json:"handle"`
	ShortID   string    `json:"short_id"`
	Name      string    `json:"name"`
	Tag       string    `json:"tag"`
	CreatedAt time.Time `json:"created_at"`

	Devices  int        `json:"devices"`
	LastSeen *time.Time `json:"last_seen"`

	// Stale marks a group none of whose devices has connected for longer
	// than the stale threshold.
	Stale bool `json:"stale"`

	Entries int64      `json:"entries"`
	Oldest  *time.Time `json:"oldest"`

	// LimitBytes is the cap the group runs into (zero: none), and LimitSource
	// where it comes from: "group", "tag" or "default".
	LimitBytes  int64  `json:"limit_bytes"`
	LimitSource string `json:"limit_source"`

	// Usage is nil when blob storage is not accounted.
	Usage *usageJSON `json:"usage"`
}

type usageJSON struct {
	UsedBytes int64 `json:"used_bytes"`

	// OverLimit marks a group holding more than its limit, which happens
	// after the limit is lowered: its next upload evicts down to it.
	OverLimit bool `json:"over_limit"`
}

// groupsJSON is the groups screen: one page of the groups, searched, sorted
// and filtered, and what the screen shows around the table.
type groupsJSON struct {
	tablePage[groupJSON]

	// Tags is the filter row: every tag, even an empty one, then the
	// untagged groups when there are tags at all. Its counts ignore the
	// search, so a filter always says how many groups it holds.
	Tags []tagChipJSON `json:"tags"`

	// Filter is the tag the list is narrowed to: "" for all, "-" for the
	// untagged groups, or a tag name. Stale narrows it to stale groups.
	Filter string `json:"filter"`
	Stale  bool   `json:"stale"`

	GroupCount     int  `json:"group_count"`
	StaleCount     int  `json:"stale_count"`
	StaleAfterDays int  `json:"stale_after_days"`
	HasUsage       bool `json:"has_usage"`

	Problems []string `json:"problems"`
}

// tagChipJSON is one entry of the groups screen's tag filter.
type tagChipJSON struct {
	Name   string `json:"name"`
	Filter string `json:"filter"`
	Groups int    `json:"groups"`
}

// problems collects the parts of a view that could not be read, and logs
// each one.
type problems struct {
	s    *Server
	ctx  context.Context
	view string
	list []string
}

func (s *Server) problems(ctx context.Context, view string) *problems {
	return &problems{s: s, ctx: ctx, view: view, list: []string{}}
}

func (p *problems) add(what string, err error) {
	p.s.logger.ErrorContext(p.ctx, p.view+": "+what, "error", err)
	p.list = append(p.list, "Could not read "+what+".")
}

// handleDashboard returns the dashboard (SPEC §4.4): the summary cards, the
// health card and storage. The groups are on their own screen.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, _ session) {
	ctx := r.Context()
	d := s.dashboard
	now := s.now()
	probs := s.problems(ctx, "dashboard")
	view := dashboardJSON{CanSweep: s.sweep != nil}

	if d.Ledger != nil {
		view.Storage = s.storageView(ctx, probs)
	}
	view.Health = s.health(ctx, now)
	view.UnusedTokens = s.unusedTokens(ctx, now, probs)

	settings := s.settings(ctx)
	view.StaleAfterDays = wholeDays(settings.StaleAfter)
	if groups, err := s.store.ListGroups(ctx); err != nil {
		probs.add("the groups", err)
	} else {
		tags, err := s.tagMap(ctx)
		if err != nil {
			probs.add("the tags", err)
		}
		for _, g := range groups {
			row, _ := s.groupRow(ctx, g, tags, settings, now, probs)
			view.DeviceCount += row.Devices
			if row.Stale {
				view.StaleCount++
			}
		}
		view.GroupCount = len(groups)
		view.Store = s.storeTotals(ctx, groups, probs)
	}
	view.Problems = probs.list
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) unusedTokens(ctx context.Context, now time.Time, probs *problems) int {
	tokens, err := s.store.ListTokens(ctx)
	if err != nil {
		probs.add("the tokens", err)
		return 0
	}
	n := 0
	for _, t := range tokens {
		if !t.Used && !t.Expired(now) {
			n++
		}
	}
	return n
}

// groupItem is a group's row with what the list needs that the row does not
// show: the id, for a stable order, and the device names, for the search.
type groupItem struct {
	row     groupJSON
	id      string
	devices []string
}

func (g groupItem) json() groupJSON { return g.row }

// groupTable is the groups list: searched by name, tag, short id and device
// name, heaviest first by default.
var groupTable = table[groupItem]{
	sorts: map[string]sortKey[groupItem]{
		"name":        {cmp: func(a, b groupItem) int { return byText(a.row.Name, b.row.Name) }},
		"tag":         {cmp: func(a, b groupItem) int { return byText(a.row.Tag, b.row.Tag) }},
		"devices":     {cmp: func(a, b groupItem) int { return byNumber(a.row.Devices, b.row.Devices) }, desc: true},
		"last_active": {cmp: func(a, b groupItem) int { return byTime(a.row.LastSeen, b.row.LastSeen) }, desc: true},
		"entries":     {cmp: func(a, b groupItem) int { return byNumber(a.row.Entries, b.row.Entries) }, desc: true},
		"oldest":      {cmp: func(a, b groupItem) int { return byTime(a.row.Oldest, b.row.Oldest) }, desc: true},
		"storage":     {cmp: func(a, b groupItem) int { return byNumber(usedBytes(a.row), usedBytes(b.row)) }, desc: true},
	},
	defaultSort: "storage",
	fields: func(g groupItem) []string {
		return append([]string{g.row.Name, g.row.Tag, g.row.ShortID}, g.devices...)
	},
	tie: func(a, b groupItem) int {
		if c := byText(a.row.Name, b.row.Name); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	},
}

// handleGroupList returns one page of the groups screen. Beyond the list
// parameters it takes ?tag=name (or ?tag=- for the untagged groups) and
// ?stale=1.
func (s *Server) handleGroupList(w http.ResponseWriter, r *http.Request, _ session) {
	ctx := r.Context()
	now := s.now()
	query := r.URL.Query()
	filter := query.Get("tag")
	staleOnly := query.Get("stale") == "1"
	probs := s.problems(ctx, "groups")
	view := groupsJSON{
		Tags:     []tagChipJSON{},
		Filter:   filter,
		Stale:    staleOnly,
		HasUsage: s.dashboard.Ledger != nil,
	}

	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		probs.add("the groups", err)
		view.tablePage = mapPage(groupTable.run(nil, query), groupItem.json)
		view.Problems = probs.list
		writeJSON(w, http.StatusOK, view)
		return
	}
	tags, err := s.tagMap(ctx)
	if err != nil {
		probs.add("the tags", err)
	}
	settings := s.settings(ctx)
	view.StaleAfterDays = wholeDays(settings.StaleAfter)

	counts := make(map[string]int)
	items := make([]groupItem, 0, len(groups))
	for _, g := range groups {
		row, devices := s.groupRow(ctx, g, tags, settings, now, probs)
		if row.Stale {
			view.StaleCount++
		}
		key := g.Tag
		if key == "" {
			key = "-"
		}
		counts[key]++
		if (filter != "" && filter != key) || (staleOnly && !row.Stale) {
			continue
		}
		item := groupItem{row: row, id: g.ID, devices: make([]string, 0, len(devices))}
		for _, d := range devices {
			item.devices = append(item.devices, d.Name)
		}
		items = append(items, item)
	}
	view.GroupCount = len(groups)

	names := make([]string, 0, len(tags))
	for name := range tags {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		view.Tags = append(view.Tags, tagChipJSON{Name: name, Filter: name, Groups: counts[name]})
	}
	if n := counts["-"]; n > 0 && len(names) > 0 {
		view.Tags = append(view.Tags, tagChipJSON{Name: "Untagged", Filter: "-", Groups: n})
	}

	view.tablePage = mapPage(groupTable.run(items, query), groupItem.json)
	view.Problems = probs.list
	writeJSON(w, http.StatusOK, view)
}

func usedBytes(g groupJSON) int64 {
	if g.Usage == nil {
		return 0
	}
	return g.Usage.UsedBytes
}

// settings reads the runtime settings, falling back to the defaults when they
// cannot be read: a stale badge computed from the default is better than no
// page.
func (s *Server) settings(ctx context.Context) store.Settings {
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "read settings", "error", err)
		return store.Settings{StaleAfter: store.DefaultStaleAfter}
	}
	return st
}

// groupRow builds one group's row: devices, activity, entries and storage. It
// also returns the devices, which the group page lists.
func (s *Server) groupRow(ctx context.Context, g store.Group, tags map[string]store.Tag, settings store.Settings, now time.Time, probs *problems) (groupJSON, []store.Device) {
	d := s.dashboard
	v := groupJSON{
		Handle:    handleOf(g.ID),
		ShortID:   shortID(g.ID),
		Name:      g.Name,
		Tag:       g.Tag,
		CreatedAt: g.CreatedAt.UTC(),
	}

	devices, err := s.store.ListDevices(ctx, g.ID)
	if err != nil {
		probs.add("the devices of "+g.Name, err)
	}
	v.Devices = len(devices)
	var last time.Time
	for _, dev := range devices {
		if dev.LastSeen.After(last) {
			last = dev.LastSeen
		}
	}
	v.LastSeen = timeOrNil(last)
	v.Stale = isStale(now, lastActive(g, devices), settings.StaleAfter)

	if d.Entries != nil {
		st, err := d.Entries.GroupStats(ctx, g.ID)
		if err != nil {
			probs.add("the entries of "+g.Name, err)
		}
		v.Entries = st.Entries
		v.Oldest = timeOrNil(st.Oldest)
	}

	l := s.effectiveLimit(g, tags)
	limit := s.shownLimit(l)
	v.LimitBytes = limit
	v.LimitSource = l.Source
	if d.Ledger != nil {
		u, err := d.Ledger.GroupUsage(ctx, g.ID)
		if err != nil {
			probs.add("the storage of "+g.Name, err)
		}
		v.Usage = &usageJSON{UsedBytes: u.Bytes, OverLimit: limit > 0 && u.Bytes > limit}
	}
	return v, devices
}

// wholeDays is a duration in whole days.
func wholeDays(d time.Duration) int { return int(d / (24 * time.Hour)) }

// days renders a duration as whole days, for the audit log.
func days(d time.Duration) string {
	n := wholeDays(d)
	return fmt.Sprintf("%d %s", n, plural(n, "day", "days"))
}

func (s *Server) storageView(ctx context.Context, probs *problems) *storageJSON {
	d := s.dashboard
	v := &storageJSON{
		Backend:         d.Backend,
		Location:        d.Location,
		LimitBytes:      d.TotalLimit,
		GroupLimitBytes: d.GroupLimit,
		EntryMaxBytes:   d.EntryMaxBytes,
		InlineMaxBytes:  d.InlineMaxBytes,
	}

	if u, err := d.Ledger.Usage(ctx); err != nil {
		probs.add("the storage usage", err)
	} else {
		v.UsedBytes = u.Bytes
		v.Entries = u.Entries
	}
	if ev, err := d.Ledger.Evictions(ctx); err != nil {
		probs.add("the eviction counts", err)
	} else {
		v.Evictions = ev.Count
		v.EvictedBytes = ev.Bytes
	}

	if d.Uploads != nil {
		n, reserved := d.Uploads()
		v.Uploads = &uploadsJSON{Count: n, ReservedBytes: reserved}
	}

	if d.SweepStatus != nil {
		st := d.SweepStatus()
		if !st.At.IsZero() {
			v.LastSweep = &sweepJSON{At: st.At.UTC(), TookMS: st.Took.Round(time.Millisecond).Milliseconds()}
			if st.Err != nil {
				v.LastSweep.Error = st.Err.Error()
			}
		}
	}
	return v
}

// humanBytes renders a byte count in binary units: "0 B", "512 B", "3.4 MiB".
// The app formats sizes itself; this is for the audit log and for messages.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	if v >= 100 {
		return fmt.Sprintf("%.0f %ciB", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f %ciB", v, "KMGTPE"[exp])
}

func limitText(n int64) string {
	if n <= 0 {
		return "no limit"
	}
	return humanBytes(n)
}

// shortID is enough of an id to tell groups or devices apart on screen. The
// full group id is the creation token's value; the token screen already
// shows it.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
