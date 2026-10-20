package admin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// dashboardView is everything the dashboard renders. It is built from counts
// and sizes only: no entry id, content type or filename reaches it, because
// the server has none of those to give (SPEC §2.3, §4.4).
type dashboardView struct {
	GroupCount   int
	DeviceCount  int
	UnusedTokens int

	// Storage is nil when no ledger is wired.
	Storage *storageView

	// Health is the state of the server and of Redis.
	Health *healthView

	// Groups are the rows shown: every group, or the ones under Filter.
	Groups []groupView

	// Tags totals every tag, plus the untagged groups, whatever the filter.
	Tags []tagSummary

	// Filter is the tag the table is narrowed to: "" for all, "-" for the
	// untagged groups, or a tag name. FilterName is how the page says it.
	Filter     string
	FilterName string

	// StaleCount is how many of all groups are stale, and StaleAfter how
	// long that means.
	StaleCount int
	StaleAfter string

	// CanSweep shows the "Sweep now" button.
	CanSweep bool

	// Problems lists the parts that could not be read. The rest of the page
	// still renders: a dashboard that fails whole when one Redis call does
	// is useless exactly when it is needed.
	Problems []string
}

// tagSummary is one row of the dashboard's per-tag totals.
type tagSummary struct {
	Name    string
	Filter  string
	Current bool
	Groups  int
	Devices int
	Used    string
	Limit   string
}

type storageView struct {
	Backend  string
	Location string

	Used       string
	UsedBytes  int64
	Limit      string
	LimitBytes int64
	HighBytes  int64
	Percent    int
	Entries    int64

	GroupLimit string
	EntryMax   string
	InlineMax  string

	Evictions    int64
	EvictedBytes string

	HasUploads    bool
	Uploads       int
	ReservedBytes string

	Swept     bool
	SweepAt   string
	SweepTook string
	SweepErr  string
}

type groupView struct {
	ID      string
	Path    string
	Name    string
	Tag     string
	ShortID string
	Created string

	Devices  int
	LastSeen string

	// Stale marks a group none of whose devices has connected for longer
	// than the stale threshold.
	Stale bool

	Entries int64
	Oldest  string

	// Blob storage against the group's effective limit. HasUsage is false
	// when no ledger is wired.
	HasUsage    bool
	Used        string
	UsedBytes   int64
	LimitBytes  int64
	HighBytes   int64
	Percent     int
	LimitText   string
	LimitSource string

	// OverLimit marks a group holding more than its limit, which happens
	// after the limit is lowered: its next upload evicts down to it.
	OverLimit bool
}

// renderDashboard renders the dashboard for an authenticated session.
func (s *Server) renderDashboard(w http.ResponseWriter, r *http.Request, sess session) {
	data := s.page(sess, "Dashboard", "dashboard")
	data.Dashboard = s.buildDashboard(r.Context(), r.URL.Query().Get("tag"))
	s.render(w, r, http.StatusOK, "dashboard.html", data)
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
func (s *Server) groupRow(ctx context.Context, g store.Group, tags map[string]store.Tag, settings store.Settings, now time.Time, problem func(string, error)) (groupView, []store.Device) {
	d := s.dashboard
	v := groupView{
		ID:      g.ID,
		Path:    groupPath(g.ID),
		Name:    g.Name,
		Tag:     g.Tag,
		ShortID: shortID(g.ID),
		Created: g.CreatedAt.UTC().Format("2006-01-02"),
	}

	devices, err := s.store.ListDevices(ctx, g.ID)
	if err != nil {
		problem("the devices of "+g.Name, err)
	}
	v.Devices = len(devices)
	var last time.Time
	for _, dev := range devices {
		if dev.LastSeen.After(last) {
			last = dev.LastSeen
		}
	}
	v.LastSeen = ago(now, last, "never")
	v.Stale = isStale(now, lastActive(g, devices), settings.StaleAfter)

	if d.Entries != nil {
		st, err := d.Entries.GroupStats(ctx, g.ID)
		if err != nil {
			problem("the entries of "+g.Name, err)
		}
		v.Entries = st.Entries
		v.Oldest = ago(now, st.Oldest, "—")
	}

	l := s.effectiveLimit(g, tags)
	limit := s.shownLimit(l)
	v.LimitBytes = limit
	v.LimitText = limitText(limit)
	v.LimitSource = l.Source
	if d.Ledger != nil {
		u, err := d.Ledger.GroupUsage(ctx, g.ID)
		if err != nil {
			problem("the storage of "+g.Name, err)
		}
		v.HasUsage = true
		v.Used = humanBytes(u.Bytes)
		v.UsedBytes = u.Bytes
		v.HighBytes = limit * 9 / 10
		v.Percent = percent(u.Bytes, limit)
		v.OverLimit = limit > 0 && u.Bytes > limit
	}
	return v, devices
}

func (s *Server) buildDashboard(ctx context.Context, filter string) *dashboardView {
	d := s.dashboard
	now := s.now()
	view := &dashboardView{CanSweep: s.sweep != nil}
	problem := func(what string, err error) {
		s.logger.ErrorContext(ctx, "dashboard: "+what, "error", err)
		view.Problems = append(view.Problems, "Could not read "+what+".")
	}

	if d.Ledger != nil {
		view.Storage = s.storageView(ctx, now, problem)
	}
	view.Health = s.health(ctx, now)

	tokens, err := s.store.ListTokens(ctx)
	if err != nil {
		problem("the tokens", err)
	}
	for _, t := range tokens {
		if !t.Used && !t.Expired(now) {
			view.UnusedTokens++
		}
	}

	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		problem("the groups", err)
		return view
	}
	tags, err := s.tagMap(ctx)
	if err != nil {
		problem("the tags", err)
	}
	settings := s.settings(ctx)
	view.StaleAfter = days(settings.StaleAfter)

	type total struct {
		groups, devices int
		used            int64
	}
	totals := make(map[string]*total)
	for _, g := range groups {
		row, _ := s.groupRow(ctx, g, tags, settings, now, problem)
		view.DeviceCount += row.Devices
		if row.Stale {
			view.StaleCount++
		}
		key := g.Tag
		if key == "" {
			key = "-"
		}
		t := totals[key]
		if t == nil {
			t = &total{}
			totals[key] = t
		}
		t.groups++
		t.devices += row.Devices
		t.used += row.UsedBytes

		if filter == "" || filter == key {
			view.Groups = append(view.Groups, row)
		}
	}
	view.GroupCount = len(groups)

	// Every tag, even an empty one, then the untagged groups.
	names := make([]string, 0, len(tags))
	for name := range tags {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := totals[name]
		if t == nil {
			t = &total{}
		}
		lim := "default"
		if tag := tags[name]; tag.Limit.Enabled {
			lim = limitText(tag.Limit.Bytes)
		}
		view.Tags = append(view.Tags, tagSummary{
			Name: name, Filter: name, Current: filter == name,
			Groups: t.groups, Devices: t.devices, Used: humanBytes(t.used), Limit: lim,
		})
	}
	if t := totals["-"]; t != nil && len(names) > 0 {
		view.Tags = append(view.Tags, tagSummary{
			Name: "Untagged", Filter: "-", Current: filter == "-",
			Groups: t.groups, Devices: t.devices, Used: humanBytes(t.used), Limit: "default",
		})
	}

	switch filter {
	case "":
	case "-":
		view.Filter, view.FilterName = filter, "untagged groups"
	default:
		view.Filter, view.FilterName = filter, "tag “"+filter+"”"
	}

	// Heaviest first: the group closest to its limit is the one to look at.
	sort.SliceStable(view.Groups, func(i, j int) bool {
		return view.Groups[i].UsedBytes > view.Groups[j].UsedBytes
	})
	return view
}

// days renders a duration as whole days.
func days(d time.Duration) string {
	n := int(d / (24 * time.Hour))
	return fmt.Sprintf("%d %s", n, plural(n, "day", "days"))
}

func (s *Server) storageView(ctx context.Context, now time.Time, problem func(string, error)) *storageView {
	d := s.dashboard
	v := &storageView{
		Backend:    d.Backend,
		Location:   d.Location,
		Limit:      limitText(d.TotalLimit),
		LimitBytes: d.TotalLimit,
		HighBytes:  d.TotalLimit * 9 / 10,
		GroupLimit: limitText(d.GroupLimit),
		EntryMax:   humanBytes(d.EntryMaxBytes),
		InlineMax:  humanBytes(d.InlineMaxBytes),
	}

	if u, err := d.Ledger.Usage(ctx); err != nil {
		problem("the storage usage", err)
	} else {
		v.Used = humanBytes(u.Bytes)
		v.UsedBytes = u.Bytes
		v.Entries = u.Entries
		v.Percent = percent(u.Bytes, d.TotalLimit)
	}
	if ev, err := d.Ledger.Evictions(ctx); err != nil {
		problem("the eviction counts", err)
	} else {
		v.Evictions = ev.Count
		v.EvictedBytes = humanBytes(ev.Bytes)
	}

	if d.Uploads != nil {
		n, reserved := d.Uploads()
		v.HasUploads = true
		v.Uploads = n
		v.ReservedBytes = humanBytes(reserved)
	}

	if d.SweepStatus != nil {
		st := d.SweepStatus()
		if !st.At.IsZero() {
			v.Swept = true
			v.SweepAt = ago(now, st.At, "")
			v.SweepTook = st.Took.Round(time.Millisecond).String()
			if st.Err != nil {
				v.SweepErr = st.Err.Error()
			}
		}
	}
	return v
}

// humanBytes renders a byte count in binary units: "0 B", "512 B", "3.4 MiB".
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

// percent is used/limit as a whole percentage, capped at 100; 0 when there is
// no limit.
func percent(used, limit int64) int {
	if limit <= 0 || used <= 0 {
		return 0
	}
	p := used * 100 / limit
	if p > 100 {
		p = 100
	}
	return int(p)
}

// ago renders how long before now t was, coarsely: "just now", "12 min ago",
// "3 h ago", "2 days ago". A zero t renders as zero.
func ago(now, t time.Time, zero string) string {
	if t.IsZero() {
		return zero
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
	}
}

// shortID is enough of a group id to tell groups apart on screen. The full id
// is the creation token's value; the token screen already shows it.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
