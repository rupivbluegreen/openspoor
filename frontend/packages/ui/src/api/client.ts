import { ApiError, toApiError } from './errors';
import { safeStorage } from '../storage';

export type QueryValue = string | number | boolean | undefined | null;
export type Query = Record<string, QueryValue | QueryValue[]>;

export interface Page<T> {
  items: T[];
  total: number;
}

export interface RequestOptions {
  query?: Query;
  headers?: Record<string, string>;
  /** Raw body (string/Blob/FormData). When set, `json` is ignored. */
  body?: BodyInit;
  json?: unknown;
  signal?: AbortSignal;
  /** How to read the response. Defaults to JSON (falls back to text). */
  responseType?: 'json' | 'text' | 'blob';
  /** Skip the Authorization header (public endpoints). */
  anonymous?: boolean;
}

export interface ApiClientOptions {
  baseUrl?: string;
  getToken?: () => string | null;
  onUnauthorized?: (err: ApiError) => void;
  fetchImpl?: typeof fetch;
}

export function buildQuery(query?: Query): string {
  if (!query) return '';
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    const values = Array.isArray(v) ? v : [v];
    for (const x of values) {
      if (x === undefined || x === null || x === '') continue;
      p.append(k, String(x));
    }
  }
  const s = p.toString();
  return s ? `?${s}` : '';
}

/** Normalises list responses: `{items,total}`, a bare array, or null. */
export function toPage<T>(data: unknown): Page<T> {
  if (Array.isArray(data)) return { items: data as T[], total: data.length };
  if (data && typeof data === 'object' && Array.isArray((data as { items?: unknown }).items)) {
    const d = data as { items: T[]; total?: number };
    return { items: d.items, total: typeof d.total === 'number' ? d.total : d.items.length };
  }
  return { items: [], total: 0 };
}

export function createApiClient(opts: ApiClientOptions = {}) {
  const baseUrl = opts.baseUrl ?? '/api/v1';

  async function request<T>(method: string, path: string, ro: RequestOptions = {}): Promise<T> {
    const headers: Record<string, string> = { Accept: 'application/json, application/problem+json;q=0.9, */*;q=0.5', ...ro.headers };
    let body: BodyInit | undefined = ro.body;
    if (body === undefined && ro.json !== undefined) {
      body = JSON.stringify(ro.json);
      headers['Content-Type'] = headers['Content-Type'] ?? 'application/json';
    }
    const token = ro.anonymous ? null : opts.getToken?.();
    if (token) headers.Authorization = `Bearer ${token}`;
    const doFetch = opts.fetchImpl ?? globalThis.fetch.bind(globalThis);
    let res: Response;
    try {
      res = await doFetch(baseUrl + path + buildQuery(ro.query), { method, headers, body, signal: ro.signal });
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') throw e;
      throw new ApiError(0, 'Network error', { detail: e instanceof Error ? e.message : String(e) });
    }
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      let parsed: unknown = text;
      try {
        parsed = text ? JSON.parse(text) : undefined;
      } catch {
        /* plain text */
      }
      const err = toApiError(res.status, res.statusText, parsed);
      if (res.status === 401 && !ro.anonymous) opts.onUnauthorized?.(err);
      throw err;
    }
    if (ro.responseType === 'blob') return (await res.blob()) as T;
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    if (ro.responseType === 'text') return text as T;
    if (!text) return undefined as T;
    try {
      return JSON.parse(text) as T;
    } catch {
      return text as T;
    }
  }

  return {
    request,
    get: <T>(path: string, query?: Query, ro?: RequestOptions) => request<T>('GET', path, { ...ro, query }),
    list: async <T>(path: string, query?: Query, ro?: RequestOptions) => toPage<T>(await request<unknown>('GET', path, { ...ro, query })),
    post: <T>(path: string, json?: unknown, ro?: RequestOptions) => request<T>('POST', path, { ...ro, json: json ?? {} }),
    put: <T>(path: string, json?: unknown, ro?: RequestOptions) => request<T>('PUT', path, { ...ro, json }),
    patch: <T>(path: string, json?: unknown, ro?: RequestOptions) => request<T>('PATCH', path, { ...ro, json }),
    del: <T = void>(path: string, ro?: RequestOptions) => request<T>('DELETE', path, ro),
  };
}

export type ApiClient = ReturnType<typeof createApiClient>;

const TOKEN_KEY = 'openspoor.token';
type Listener = (token: string | null) => void;
const listeners = new Set<Listener>();

/** Session token store (localStorage-backed, falls back to memory). */
export const tokenStore = {
  memory: null as string | null,
  key: TOKEN_KEY,
  get(): string | null {
    return safeStorage.get(this.key) ?? this.memory;
  },
  set(token: string | null): void {
    this.memory = token;
    safeStorage.set(this.key, token);
    listeners.forEach((l) => l(token));
  },
  subscribe(l: Listener): () => void {
    listeners.add(l);
    return () => listeners.delete(l);
  },
};

/** Shared client used by all apps. A 401 clears the session. */
export const api = createApiClient({
  getToken: () => tokenStore.get(),
  onUnauthorized: () => tokenStore.set(null),
});

/** A fresh Idempotency-Key value. */
export function idempotencyKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  if (c && typeof c.randomUUID === 'function') return c.randomUUID();
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}
