import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import type { ApiKey, ApiKeyList } from "../api";
import { RouterProvider } from "../router";
import { ApiKeysPage } from "./ApiKeysPage";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const backup: ApiKey = {
  id: "abcdefgh",
  name: "Backup script",
  scopes: ["read", "tokens"],
  created_at: "2026-11-01T10:00:00Z",
  expires_at: "2027-01-30T10:00:00Z",
  last_used_at: null,
  last_used_ip: "",
  created_by: "cli",
  status: "active",
};

const list = (rows: ApiKey[]): ApiKeyList => ({
  rows,
  total: rows.length,
  page: 1,
  size: 25,
  sort: "created",
  dir: "desc",
  max: 50,
  count: rows.length,
  scopes: ["read", "tokens", "admin"],
  expiry_days: [30, 90, 365, 0],
  default_expiry_days: 90,
});

const newKey = `tpp_ak_ijklmnop_${"a".repeat(52)}`;

/** stubRelay answers the screen's requests and records the changes. */
function stubRelay() {
  const rows = [backup];
  const changes: { method: string; url: string; body: unknown }[] = [];
  const fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (method === "GET") return json(list(rows));
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    changes.push({ method, url, body });
    if (method === "POST") {
      const created: ApiKey = { ...backup, id: "ijklmnop", name: body.name, scopes: body.scopes, created_by: "admin" };
      rows.unshift(created);
      return json({ key: newKey, api_key: created }, 201);
    }
    rows.splice(
      rows.findIndex((r) => url.endsWith(`/${r.id}`)),
      1,
    );
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fetch);
  return changes;
}

function renderPage() {
  window.history.replaceState(null, "", "/admin/api-keys");
  return render(
    <RouterProvider>
      <ApiKeysPage />
    </RouterProvider>,
  );
}

describe("ApiKeysPage", () => {
  test("lists keys by name and id, never by key", async () => {
    stubRelay();
    renderPage();
    expect(await screen.findByText("Backup script")).toBeTruthy();
    expect(screen.getByText("abcdefgh")).toBeTruthy();
    expect(screen.getByRole("link", { name: "API docs" }).getAttribute("href")).toContain("/two-place-paste/api/");
  });

  test("creates a key and shows it once", async () => {
    const changes = stubRelay();
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Backup script");

    await user.type(screen.getByLabelText("Name"), "Monitoring");
    await user.click(screen.getByRole("checkbox", { name: /^tokens / }));
    await user.click(screen.getByRole("button", { name: "Create key" }));

    await waitFor(() => expect(changes).toHaveLength(1));
    expect(changes[0]).toMatchObject({
      method: "POST",
      url: "/admin/api/api-keys",
      body: { name: "Monitoring", scopes: ["read", "tokens"], expiry_days: 90 },
    });
    expect(await screen.findByText(newKey)).toBeTruthy();
    expect(screen.getByText(/shown only this once/)).toBeTruthy();
  });

  test("revokes a key after asking", async () => {
    const changes = stubRelay();
    const user = userEvent.setup();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderPage();
    await screen.findByText("Backup script");

    await user.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(changes).toHaveLength(1));
    expect(changes[0]).toMatchObject({ method: "DELETE", url: "/admin/api/api-keys/abcdefgh" });
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("Backup script"));
  });
});
