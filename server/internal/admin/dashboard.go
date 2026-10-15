package admin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"
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

	Groups []groupView

	// Problems lists the parts that could not be read. The rest of the page
	// still renders: a dashboard that fails whole when one Redis call does
	// is useless exactly when it is needed.
	Problems []string
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

	Swept     bool
	SweepAt   string
	SweepTook string
	SweepErr  string
}

type groupView struct {
	Name    string
	ShortID string
	Created string

	Devices  int
	LastSeen string

	Entries int64
	Oldest  string

	// Blob storage against the group limit. HasUsage is false when no
	// ledger is wired.
	HasUsage   bool
	Used       string
	UsedBytes  int64
	LimitBytes int64
	HighBytes  int64
	Percent    int
}

// renderDashboard renders the dashboard for an authenticated session.
func (s *Server) renderDashboard(w http.ResponseWriter, r *http.Request, sess session) {
	data := pageData{Title: "Dashboard", Authenticated: true, CSRF: sess.csrf, Nav: "dashboard"}
	data.Dashboard = s.buildDashboard(r.Context())
	s.render(w, r, http.StatusOK, "dashboard.html", data)
}

func (s *Server) buildDashboard(ctx context.Context) *dashboardView {
	d := s.dashboard
	now := s.now()
	view := &dashboardView{}
	problem := func(what string, err error) {
		s.logger.ErrorContext(ctx, "dashboard: "+what, "error", err)
		view.Problems = append(view.Problems, "Could not read "+what+".")
	}

	if d.Ledger != nil {
		view.Storage = s.storageView(ctx, now, problem)
	}

	tokens, err := s.store.ListTokens(ctx)
	if err != nil {
		problem("the groups", err)
		return view
	}
	for _, t := range tokens {
		if !t.Used {
			view.UnusedTokens++
			continue
		}
		g := groupView{
			Name:    t.Name,
			ShortID: shortID(t.GroupID),
			Created: t.CreatedAt.UTC().Format("2006-01-02"),
		}

		devices, err := s.store.ListDevices(ctx, t.GroupID)
		if err != nil {
			problem("the devices of "+t.Name, err)
		}
		g.Devices = len(devices)
		var last time.Time
		for _, dev := range devices {
			if dev.LastSeen.After(last) {
				last = dev.LastSeen
			}
		}
		g.LastSeen = ago(now, last, "never")
		view.DeviceCount += len(devices)

		if d.Entries != nil {
			st, err := d.Entries.GroupStats(ctx, t.GroupID)
			if err != nil {
				problem("the entries of "+t.Name, err)
			}
			g.Entries = st.Entries
			g.Oldest = ago(now, st.Oldest, "—")
		}

		if d.Ledger != nil {
			u, err := d.Ledger.GroupUsage(ctx, t.GroupID)
			if err != nil {
				problem("the storage of "+t.Name, err)
			}
			limit := d.GroupLimit
			if d.TotalLimit > 0 && (limit == 0 || d.TotalLimit < limit) {
				limit = d.TotalLimit
			}
			g.HasUsage = true
			g.Used = humanBytes(u.Bytes)
			g.UsedBytes = u.Bytes
			g.LimitBytes = limit
			g.HighBytes = limit * 9 / 10
			g.Percent = percent(u.Bytes, limit)
		}
		view.Groups = append(view.Groups, g)
	}
	view.GroupCount = len(view.Groups)

	// Heaviest first: the group closest to its limit is the one to look at.
	sort.SliceStable(view.Groups, func(i, j int) bool {
		return view.Groups[i].UsedBytes > view.Groups[j].UsedBytes
	})
	return view
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
