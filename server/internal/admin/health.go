package admin

import (
	"bufio"
	"context"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// healthView is the dashboard's health card: the running build, and whether
// Redis is set up the way SPEC §4.2 needs it. A relay on `allkeys-lru` or
// without AOF looks healthy until the day it silently loses every group, so
// the card says so up front.
type healthView struct {
	Version   string
	Uptime    string
	GoVersion string

	HasRedis bool

	Policy      string
	PolicyState string // good, warn, bad or unknown
	PolicyNote  string

	AOF      string
	AOFState string
	AOFNote  string

	Memory    string
	MaxMemory string
}

// health reads the health card. Every Redis read is allowed to fail: a
// managed Redis may refuse CONFIG, and the card then says "unknown" rather
// than taking the dashboard down with it.
func (s *Server) health(ctx context.Context, now time.Time) *healthView {
	v := &healthView{
		Version:   s.version,
		Uptime:    uptime(now.Sub(s.startedAt)),
		GoVersion: runtime.Version(),
	}
	if v.Version == "" {
		v.Version = "unknown"
	}
	if s.redis == nil {
		return v
	}
	v.HasRedis = true

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	v.Policy, v.PolicyState, v.PolicyNote = "unknown", "unknown", "Redis did not allow reading its configuration."
	if cfg, err := s.redis.ConfigGet(ctx, "maxmemory-policy").Result(); err == nil {
		if p, ok := cfg["maxmemory-policy"]; ok {
			v.Policy = p
			v.PolicyState, v.PolicyNote = policyState(p)
		}
	}

	v.AOF, v.AOFState, v.AOFNote = "unknown", "unknown", "Redis did not allow reading its configuration."
	if cfg, err := s.redis.ConfigGet(ctx, "appendonly").Result(); err == nil {
		if a, ok := cfg["appendonly"]; ok {
			v.AOF = a
			if a == "yes" {
				v.AOFState, v.AOFNote = "good", ""
			} else {
				v.AOFState, v.AOFNote = "bad", "Without AOF a Redis restart can lose groups and pairings. Set appendonly yes."
			}
		}
	}

	v.Memory, v.MaxMemory = "unknown", "unknown"
	if info, err := s.redis.Info(ctx, "memory").Result(); err == nil {
		fields := infoFields(info)
		if m := fields["used_memory_human"]; m != "" {
			v.Memory = m
		}
		switch m := fields["maxmemory_human"]; m {
		case "":
		case "0B":
			v.MaxMemory = "no limit"
		default:
			v.MaxMemory = m
		}
	}
	return v
}

// policyState judges a maxmemory-policy against SPEC §4.2.
func policyState(p string) (state, note string) {
	switch {
	case p == "volatile-lru":
		return "good", ""
	case strings.HasPrefix(p, "allkeys-"):
		return "bad", "Under memory pressure Redis may evict groups and devices. Use volatile-lru."
	case p == "noeviction":
		return "warn", "Nothing is ever evicted: once Redis is full, new entries fail. volatile-lru is recommended."
	default:
		return "warn", "Groups are safe, but volatile-lru is the recommended policy."
	}
}

// infoFields parses the "key:value" lines of an INFO reply.
func infoFields(info string) map[string]string {
	out := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(info))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[k] = v
		}
	}
	return out
}

// uptime renders how long the server has run, coarsely.
func uptime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		n := int(d / time.Minute)
		return plural(n, "1 minute", strconv.Itoa(n)+" minutes")
	case d < 48*time.Hour:
		n := int(d / time.Hour)
		return plural(n, "1 hour", strconv.Itoa(n)+" hours")
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + " days"
	}
}
