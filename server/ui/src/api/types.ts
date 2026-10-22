// The admin API's JSON, as server/internal/admin writes it. Sizes are bytes
// and times are RFC 3339 strings in UTC; null means "none" or "never".

export interface Session {
  csrf: string;
}

export interface Limit {
  enabled: boolean;
  bytes: number;
}

/** LimitChange is a storage limit form as submitted: the checkbox, and the
 *  size as typed ("5 GiB"). An empty size keeps the stored one. */
export interface LimitChange {
  enabled: boolean;
  size: string;
}

export interface Dashboard {
  group_count: number;
  device_count: number;
  unused_tokens: number;
  stale_count: number;
  stale_after_days: number;
  storage: Storage | null;
  health: Health;
  can_sweep: boolean;
  problems: string[];
}

export interface Storage {
  backend: string;
  location: string;
  used_bytes: number;
  limit_bytes: number;
  entries: number;
  group_limit_bytes: number;
  entry_max_bytes: number;
  inline_max_bytes: number;
  evictions: number;
  evicted_bytes: number;
  uploads: { count: number; reserved_bytes: number } | null;
  last_sweep: { at: string; took_ms: number; error?: string } | null;
}

export type HealthState = "good" | "warn" | "bad" | "unknown";

export interface Health {
  version: string;
  uptime_seconds: number;
  go_version: string;
  redis: {
    policy: string;
    policy_state: HealthState;
    policy_note: string;
    aof: string;
    aof_state: HealthState;
    aof_note: string;
    memory: string;
    max_memory: string;
  } | null;
}

export type LimitSource = "group" | "tag" | "default";

export interface Group {
  id: string;
  short_id: string;
  name: string;
  tag: string;
  created_at: string;
  devices: number;
  last_seen: string | null;
  stale: boolean;
  entries: number;
  oldest: string | null;
  /** The cap the group runs into; zero means none. */
  limit_bytes: number;
  limit_source: LimitSource;
  /** Null when blob storage is not accounted. */
  usage: { used_bytes: number; over_limit: boolean } | null;
}

export interface GroupList {
  groups: Group[];
  tags: { name: string; filter: string; groups: number }[];
  filter: string;
  group_count: number;
  stale_count: number;
  stale_after_days: number;
  has_usage: boolean;
  problems: string[];
}

export interface Device {
  short_id: string;
  name: string;
  created_at: string;
  last_seen: string | null;
  online: boolean;
}

export interface GroupDetail {
  group: Group;
  devices: Device[];
  own_limit: Limit;
  tag_limit_bytes: number | null;
  default_limit_bytes: number;
  tags: string[];
  can_purge: boolean;
  problems: string[];
}

export interface Tag {
  name: string;
  groups: number;
  devices: number;
  used_bytes: number;
  limit: Limit;
}

export interface TagList {
  tags: Tag[];
  default_limit_bytes: number;
  total_limit_bytes: number;
  problems: string[];
}

export type TokenStatus = "unused" | "used" | "expired" | "group_deleted";

export interface Token {
  /** The token itself: a credential, and the id of the group it created. */
  value: string;
  name: string;
  note: string;
  tag: string;
  created_at: string;
  status: TokenStatus;
  expires_at: string | null;
  /** The creation URL, for an unused token only. */
  url?: string;
  /** The group the token created, while it exists. */
  group_id?: string;
}

export interface TokenList {
  tokens: Token[];
  tags: string[];
}

export type Expiry = "never" | "1" | "7" | "30";

export interface NewToken {
  name: string;
  note: string;
  tag: string;
  expires: Expiry;
}

export interface Settings {
  stale_days: number;
  default_days: number;
  max_days: number;
}

export interface AuditEntry {
  at: string;
  action: string;
  target: string;
  detail: string;
  ip: string;
}

export interface AuditLog {
  entries: AuditEntry[];
  kept: number;
}
