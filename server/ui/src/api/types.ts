// The admin API's JSON, as server/internal/admin writes it. Sizes are bytes
// and times are RFC 3339 strings in UTC; null means "none" or "never".

export type SortDir = "asc" | "desc";

/** TablePage is one page of a list, and the search, sort and page the relay
 *  actually applied after clamping and fallback (server/internal/admin/table.go). */
export interface TablePage<T> {
  rows: T[];
  /** total counts every row matching the search and filters, on all pages. */
  total: number;
  page: number;
  size: number;
  sort: string;
  dir: SortDir;
}

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
  /** Null when the relay has no store. */
  store: StoreTotals | null;
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

/** StoreTotals is the dashboard's store card: what every group's store
 *  holds together, and the configured limits (SPEC §4.6). */
export interface StoreTotals {
  used_bytes: number;
  items: number;
  groups_on: number;
  /** The relay's total store limit; zero means none. */
  limit_bytes: number;
  group_limit_bytes: number;
  max_item_bytes: number;
  max_group_items: number;
}

export type HealthState = "good" | "warn" | "bad" | "unknown";

export interface Health {
  version: string;
  uptime_seconds: number;
  go_version: string;
  /** The operator's website (TPP_RELAY_OPERATOR_URL) as configured, or empty
   *  when none is set. The admin opens it as is, without a group id. */
  operator_url: string;
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
  /** handle names the group in URLs and requests. Its id is the token that
   *  created it, and never leaves the relay. */
  handle: string;
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
  /** True when the limit chosen for the group is below one entry, so
   *  limit_bytes is that floor instead. */
  limit_raised: boolean;
  /** Null when blob storage is not accounted. */
  usage: { used_bytes: number; over_limit: boolean } | null;
  /** Only on the groups list, and only when the relay has a store. */
  store?: GroupStoreRow;
}

/** GroupStoreRow is a group's store on the groups list. */
export interface GroupStoreRow {
  enabled: boolean;
  emptying: boolean;
  used_bytes: number;
  used_items: number;
  limit_bytes: number;
}

export interface GroupList extends TablePage<Group> {
  /** The tag filter: every tag, then the untagged groups. Counts ignore the
   *  search. */
  tags: { name: string; filter: string; groups: number }[];
  filter: string;
  stale: boolean;
  group_count: number;
  stale_count: number;
  stale_after_days: number;
  has_usage: boolean;
  has_store: boolean;
  problems: string[];
}

export interface Device {
  short_id: string;
  name: string;
  created_at: string;
  last_seen: string | null;
  online: boolean;
  /**
   * The protocol level the device speaks: its open socket's while online,
   * else the last one it connected with. Null before it has connected since
   * the relay began recording levels.
   */
  protocol_level: number | null;
}

/** ProtocolRange is the lowest and highest protocol level the relay accepts. */
export interface ProtocolRange {
  min: number;
  max: number;
}

export interface GroupDetail {
  group: Group;
  devices: Device[];
  protocol: ProtocolRange;
  own_limit: Limit;
  tag_limit_bytes: number | null;
  default_limit_bytes: number;
  /** The smallest limit the group may be given: one entry of the largest
   *  size. Zero when unknown. */
  min_limit_bytes: number;
  tags: string[];
  can_purge: boolean;
  /** Null when the relay has no store. */
  store: GroupStore | null;
  problems: string[];
}

/** GroupStore is a group's store: off until the admin turns it on, its
 *  usage against its own limits, and its limit override (SPEC §4.6). */
export interface GroupStore {
  enabled: boolean;
  /** True while a purge empties it in the background; closed until then. */
  emptying: boolean;
  used_bytes: number;
  used_items: number;
  /** The cap the store runs into; zero means none. */
  limit_bytes: number;
  limit_source: LimitSource;
  /** True when the limit chosen for the store is below one item, so
   *  limit_bytes is that floor instead. */
  limit_raised: boolean;
  /** The smallest store limit: one item of the largest size and its record. */
  min_limit_bytes: number;
  own_limit: Limit;
  tag_limit_bytes: number | null;
  default_limit_bytes: number;
  max_items: number;
  max_item_bytes: number;
}

export interface Tag {
  name: string;
  groups: number;
  devices: number;
  used_bytes: number;
  limit: Limit;
  store_limit: Limit;
  /** True when the tag's limit is on and below the smallest allowed, so its
   *  groups get that floor instead. */
  limit_raised: boolean;
  store_limit_raised: boolean;
  /** What the stores of the tag's groups hold together, and how many of
   *  those groups have their store on. */
  store_bytes: number;
  store_groups_on: number;
}

export interface TagList extends TablePage<Tag> {
  default_limit_bytes: number;
  total_limit_bytes: number;
  /** The smallest limit a tag may set: one entry. Zero when unknown. */
  min_limit_bytes: number;
  has_store: boolean;
  default_store_limit_bytes: number;
  total_store_limit_bytes: number;
  /** The smallest store limit: one item of the largest size and its record. */
  min_store_limit_bytes: number;
  problems: string[];
}

export type TokenStatus = "unused" | "used" | "expired" | "group_deleted";

export interface Token {
  /** handle names the token. The token itself is a credential and is never
   *  in a row; its link is fetched on request (api.tokenLink). */
  handle: string;
  name: string;
  note: string;
  tag: string;
  created_at: string;
  status: TokenStatus;
  expires_at: string | null;
  /** The group the token created, while it exists. */
  group_handle?: string;
}

/** CreatedToken is a token just minted, with its creation URL. */
export interface CreatedToken extends Token {
  url: string;
}

export interface TokenList extends TablePage<Token> {
  /** The tags a new token can start its group under. */
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
  /** label is the action in plain words. */
  label: string;
  target: string;
  detail: string;
  ip: string;
  /** actor is who did it: "" for these screens, "api_key" for the REST API,
   *  "cli" for the tpp command. actor_id and actor_name name the API key. */
  actor: "" | "api_key" | "cli";
  actor_id: string;
  actor_name: string;
  /** by says who did it, in words. */
  by: string;
}

export interface AuditLog extends TablePage<AuditEntry> {
  /** kept is how many entries the relay keeps in all. */
  kept: number;
  /** actions are the kinds of entry the filter offers. */
  actions: { action: string; label: string }[];
}

/** ApiScope is what an API key may do on the REST API under /api/v1/. */
export type ApiScope = "read" | "tokens" | "admin";

/** ApiKey is one key of the REST API, as the API keys screen lists it. The
 *  key itself is shown once, when it is created, and never again. */
export interface ApiKey {
  /** id is the public part of the key, which names it in the audit log. */
  id: string;
  name: string;
  scopes: ApiScope[];
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
  last_used_ip: string;
  /** created_by is "admin" (these screens) or "cli" (tpp apikey create). */
  created_by: string;
  status: "active" | "expired";
}

export interface ApiKeyList extends TablePage<ApiKey> {
  /** max is how many keys the relay keeps, and count how many it has. */
  max: number;
  count: number;
  scopes: ApiScope[];
  /** expiry_days are the lifetimes a key can have; 0 is never. */
  expiry_days: number[];
  default_expiry_days: number;
}

export interface NewApiKey {
  name: string;
  scopes: ApiScope[];
  expiry_days: number;
}

/** CreatedApiKey is a key just created: the one response with the key. */
export interface CreatedApiKey {
  key: string;
  api_key: ApiKey;
}
