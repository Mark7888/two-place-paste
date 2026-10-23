// The admin API, one function per endpoint (server/internal/admin/register.go
// lists the same routes).
import { request } from "./client";
import type {
  CreatedToken,
  Dashboard,
  GroupDetail,
  LimitChange,
  NewToken,
  Session,
  Settings,
} from "./types";

export { ApiError, isAbort, messageOf, request, setCsrfToken, setUnauthorizedHandler } from "./client";
export type * from "./types";

const seg = encodeURIComponent;

export const api = {
  session: (signal?: AbortSignal) => request<Session>("GET", "/session", undefined, signal),
  login: (password: string) => request<Session>("POST", "/login", { password }),
  logout: () => request<void>("POST", "/logout"),

  dashboard: (signal?: AbortSignal) => request<Dashboard>("GET", "/dashboard", undefined, signal),
  sweep: () => request<{ queued: boolean }>("POST", "/sweep"),

  // The lists — tokens, groups, tags, the audit log — are read page by page
  // by SmartTable, straight from their endpoints (components/SmartTable).
  createToken: (token: NewToken) => request<CreatedToken>("POST", "/tokens", token),
  tokenLink: (handle: string) => request<{ url: string }>("GET", `/tokens/${seg(handle)}/link`),
  revokeToken: (handle: string) => request<void>("DELETE", `/tokens/${seg(handle)}`),

  group: (handle: string, signal?: AbortSignal) =>
    request<GroupDetail>("GET", `/groups/${seg(handle)}`, undefined, signal),
  renameGroup: (handle: string, name: string) => request<void>("PUT", `/groups/${seg(handle)}/name`, { name }),
  setGroupTag: (handle: string, tag: string) => request<void>("PUT", `/groups/${seg(handle)}/tag`, { tag }),
  setGroupLimit: (handle: string, limit: LimitChange) => request<void>("PUT", `/groups/${seg(handle)}/limit`, limit),
  purgeGroup: (handle: string) => request<{ entries: number; bytes: number }>("POST", `/groups/${seg(handle)}/purge`),
  deleteGroup: (handle: string) => request<{ name: string }>("DELETE", `/groups/${seg(handle)}`),

  createTag: (name: string) => request<{ name: string }>("POST", "/tags", { name }),
  setTagLimit: (name: string, limit: LimitChange) => request<void>("PUT", `/tags/${seg(name)}/limit`, limit),
  deleteTag: (name: string) => request<void>("DELETE", `/tags/${seg(name)}`),

  settings: (signal?: AbortSignal) => request<Settings>("GET", "/settings", undefined, signal),
  saveSettings: (staleDays: number) => request<Settings>("PUT", "/settings", { stale_days: staleDays }),

};
