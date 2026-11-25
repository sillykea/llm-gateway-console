// Typed client for the console API. Every call is same-origin with the
// session cookie; a 401 anywhere sends the app back to the sign-in page.

export type Role = "admin" | "viewer";

export interface Me {
  username: string;
  role: Role;
}

export interface Point {
  t: string;
  v: number;
}

export interface Overview {
  spend_month_usd: number;
  requests: number;
  error_rate: number;
  cache_hit_rate: number;
  fallbacks: number;
  p95_seconds: number;
  open_breakers: string[];
  requests_by_hour: Point[];
  spend_by_day: Point[];
  spend_by_key: Record<string, number>;
  requests_by_model: Record<string, number>;
  window_hours: number;
  generated_at: string;
}

export interface Key {
  id: string;
  name: string;
  description: string;
  prefix: string;
  rpm: number;
  monthly_budget_usd: number;
  spend_month_usd: number | null;
  created_at: string;
  created_by: string;
  revoked_at?: string;
  revoked_by?: string;
}

export interface AuditEvent {
  at: string;
  actor: string;
  action: string;
  target?: string;
  detail?: string;
}

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

let onUnauthorized: () => void = () => {};

export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: "same-origin", headers: {} };
  if (body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (res.status === 204) {
    return undefined as T;
  }
  const data: unknown = await res.json().catch(() => ({}));
  if (!res.ok) {
    const msg =
      typeof data === "object" && data !== null && "error" in data && typeof data.error === "string"
        ? data.error
        : `Request failed (${res.status})`;
    if (res.status === 401 && path !== "/api/login") {
      onUnauthorized();
    }
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const api = {
  me: () => request<Me>("GET", "/api/me"),
  login: (username: string, password: string) => request<Me>("POST", "/api/login", { username, password }),
  logout: () => request<void>("POST", "/api/logout"),
  overview: () => request<Overview>("GET", "/api/overview"),
  keys: () => request<{ keys: Key[]; metrics_available: boolean }>("GET", "/api/keys"),
  createKey: (k: { name: string; description: string; rpm: number; monthly_budget_usd: number }) =>
    request<{ key: Key; secret: string }>("POST", "/api/keys", k),
  updateKey: (id: string, u: { description?: string; rpm?: number; monthly_budget_usd?: number }) =>
    request<Key>("PATCH", `/api/keys/${encodeURIComponent(id)}`, u),
  revokeKey: (id: string) => request<Key>("POST", `/api/keys/${encodeURIComponent(id)}/revoke`),
  audit: () => request<{ events: AuditEvent[] }>("GET", "/api/audit"),
};
