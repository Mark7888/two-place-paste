// The admin API, one function per endpoint (server/internal/admin/register.go
// lists the same routes).
import { request } from "./client";
import type {
  AuditLog,
  Dashboard,
  GroupDetail,
  GroupList,
  LimitChange,
  NewToken,
  Session,
  Settings,
  TagList,
  Token,
  TokenList,
} from "./types";

export { ApiError, isAbort, messageOf, setCsrfToken, setUnauthorizedHandler } from "./client";
export type * from "./types";

const seg = encodeURIComponent;

export const api = {
  session: (signal?: AbortSignal) => request<Session>("GET", "/session", undefined, signal),
  login: (password: string) => request<Session>("POST", "/login", { password }),
  logout: () => request<void>("POST", "/logout"),

  dashboard: (signal?: AbortSignal) => request<Dashboard>("GET", "/dashboard", undefined, signal),
  sweep: () => request<{ queued: boolean }>("POST", "/sweep"),

  tokens: (signal?: AbortSignal) => request<TokenList>("GET", "/tokens", undefined, signal),
  createToken: (token: NewToken) => request<Token>("POST", "/tokens", token),
  revokeToken: (value: string) => request<void>("DELETE", `/tokens/${seg(value)}`),

  groups: (tag: string, signal?: AbortSignal) =>
    request<GroupList>("GET", tag ? `/groups?tag=${seg(tag)}` : "/groups", undefined, signal),
  group: (id: string, signal?: AbortSignal) => request<GroupDetail>("GET", `/groups/${seg(id)}`, undefined, signal),
  renameGroup: (id: string, name: string) => request<void>("PUT", `/groups/${seg(id)}/name`, { name }),
  setGroupTag: (id: string, tag: string) => request<void>("PUT", `/groups/${seg(id)}/tag`, { tag }),
  setGroupLimit: (id: string, limit: LimitChange) => request<void>("PUT", `/groups/${seg(id)}/limit`, limit),
  purgeGroup: (id: string) => request<{ entries: number; bytes: number }>("POST", `/groups/${seg(id)}/purge`),
  deleteGroup: (id: string) => request<{ name: string }>("DELETE", `/groups/${seg(id)}`),

  tags: (signal?: AbortSignal) => request<TagList>("GET", "/tags", undefined, signal),
  createTag: (name: string) => request<{ name: string }>("POST", "/tags", { name }),
  setTagLimit: (name: string, limit: LimitChange) => request<void>("PUT", `/tags/${seg(name)}/limit`, limit),
  deleteTag: (name: string) => request<void>("DELETE", `/tags/${seg(name)}`),

  settings: (signal?: AbortSignal) => request<Settings>("GET", "/settings", undefined, signal),
  saveSettings: (staleDays: number) => request<Settings>("PUT", "/settings", { stale_days: staleDays }),

  audit: (signal?: AbortSignal) => request<AuditLog>("GET", "/audit", undefined, signal),
};
