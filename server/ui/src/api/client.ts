// The one place the app talks to the relay. Every request goes to the admin
// API under /admin/api/, on this origin, with the session cookie the browser
// holds; every change also carries the session's CSRF token, which the relay
// checks (server/internal/admin/api.go).

/** ApiError is a request the relay refused, or that never reached it
 *  (status 0). Its message is meant for the screen. */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = "ApiError";
    this.status = status;
  }
}

/** BASE is where the admin API lives. */
export const BASE = "/admin/api";

// The CSRF token of the current session. It lives in memory only: it is
// fetched from /session when the app loads and dropped on sign-out.
let csrfToken = "";

export function setCsrfToken(token: string) {
  csrfToken = token;
}

// Called when the relay says the session is gone, so the app can send the
// admin back to the sign-in screen from wherever they are.
let onUnauthorized: (() => void) | null = null;

export function setUnauthorizedHandler(handler: (() => void) | null) {
  onUnauthorized = handler;
}

type Method = "GET" | "POST" | "PUT" | "DELETE";

function errorText(body: unknown): string | null {
  if (typeof body === "object" && body !== null && "error" in body) {
    const { error } = body as { error: unknown };
    if (typeof error === "string" && error !== "") return error;
  }
  return null;
}

/** request makes one API call and returns its decoded body, or undefined for
 *  a 204. A failure throws an ApiError; a cancelled request throws the
 *  AbortError as it is, for the caller to ignore. */
export async function request<T>(method: Method, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method !== "GET") headers["X-CSRF-Token"] = csrfToken;
  if (body !== undefined) headers["Content-Type"] = "application/json";

  let res: Response;
  try {
    res = await fetch(BASE + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "same-origin",
      cache: "no-store",
      signal,
    });
  } catch (err) {
    if (signal?.aborted) throw err;
    throw new ApiError(0, "The relay could not be reached. Check the connection and try again.", { cause: err });
  }

  if (res.status === 204) return undefined as T;

  let data: unknown = null;
  try {
    data = await res.json();
  } catch (err) {
    if (signal?.aborted) throw err;
    if (res.ok) throw new ApiError(res.status, "The relay sent an answer that could not be read.", { cause: err });
  }

  if (!res.ok) {
    // A refused sign-in is a wrong password, not a lost session.
    if (res.status === 401 && path !== "/login" && path !== "/session") onUnauthorized?.();
    throw new ApiError(res.status, errorText(data) ?? `The relay answered ${res.status}.`);
  }
  return data as T;
}

/** isAbort reports whether an error is a request cancelled on purpose. */
export function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === "AbortError";
}

/** messageOf is the text to show for any error. */
export function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : "Something went wrong.";
}
