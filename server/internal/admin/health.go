package admin

import (
	"bufio"
	"context"
	"runtime"
	"strings"
	"time"
)

// healthJSON is the dashboard's health card: the running build, and whether
// Redis is set up the way SPEC §4.2 needs it. A relay on `allkeys-lru` or
// without AOF looks healthy until the day it silently loses every group, so
// the card says so up front.
type healthJSON struct {
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	GoVersion     string `json:"go_version"`

	// Redis is nil when no Redis client is wired.
	Redis *redisHealthJSON `json:"redis"`
}

type redisHealthJSON struct {
	Policy      string `json:"policy"`
	PolicyState string `json:"policy_state"` // good, warn, bad or unknown
	PolicyNote  string `json:"policy_note"`

	AOF      string `json:"aof"`
	AOFState string `json:"aof_state"` // good, bad or unknown
	AOFNote  string `json:"aof_note"`

	Memory    string `json:"memory"`
	MaxMemory string `json:"max_memory"`
}

// health reads the health card. Every Redis read is allowed to fail: a
// managed Redis may refuse CONFIG, and the card then says "unknown" rather
// than taking the dashboard down with it.
func (s *Server) health(ctx context.Context, now time.Time) *healthJSON {
	v := &healthJSON{
		Version:       s.version,
		UptimeSeconds: int64(now.Sub(s.startedAt) / time.Second),
		GoVersion:     runtime.Version(),
	}
	if v.Version == "" {
		v.Version = "unknown"
	}
	if s.redis == nil {
		return v
	}
	rv := &redisHealthJSON{}
	v.Redis = rv

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	rv.Policy, rv.PolicyState, rv.PolicyNote = "unknown", "unknown", "Redis did not allow reading its configuration."
	if cfg, err := s.redis.ConfigGet(ctx, "maxmemory-policy").Result(); err == nil {
		if p, ok := cfg["maxmemory-policy"]; ok {
			rv.Policy = p
			rv.PolicyState, rv.PolicyNote = policyState(p)
		}
	}

	rv.AOF, rv.AOFState, rv.AOFNote = "unknown", "unknown", "Redis did not allow reading its configuration."
	if cfg, err := s.redis.ConfigGet(ctx, "appendonly").Result(); err == nil {
		if a, ok := cfg["appendonly"]; ok {
			rv.AOF = a
			if a == "yes" {
				rv.AOFState, rv.AOFNote = "good", ""
			} else {
				rv.AOFState, rv.AOFNote = "bad", "Without AOF a Redis restart can lose groups and pairings. Set appendonly yes."
			}
		}
	}

	rv.Memory, rv.MaxMemory = "unknown", "unknown"
	if info, err := s.redis.Info(ctx, "memory").Result(); err == nil {
		fields := infoFields(info)
		if m := fields["used_memory_human"]; m != "" {
			rv.Memory = m
		}
		switch m := fields["maxmemory_human"]; m {
		case "":
		case "0B":
			rv.MaxMemory = "no limit"
		default:
			rv.MaxMemory = m
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
