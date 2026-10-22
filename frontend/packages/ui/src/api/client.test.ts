import { describe, expect, it, vi } from 'vitest';
import { buildQuery, createApiClient, toPage } from './client';
import { ApiError, errorMessage, toApiError } from './errors';

function jsonResponse(body: unknown, status = 200, contentType = 'application/json') {
  return new Response(typeof body === 'string' ? body : JSON.stringify(body), { status, headers: { 'Content-Type': contentType } });
}

describe('API client', () => {
  it('maps problem+json to ApiError with title, status and errors', async () => {
    const fetchImpl = vi.fn(async () =>
      jsonResponse({ title: 'Invalid EPCIS document', status: 422, errors: [{ index: 2, message: 'eventTime required' }] }, 422, 'application/problem+json'),
    );
    const client = createApiClient({ fetchImpl });
    const err = await client.post('/epcis/capture', {}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const e = err as ApiError;
    expect(e.status).toBe(422);
    expect(e.title).toBe('Invalid EPCIS document');
    expect(e.errors).toHaveLength(1);
    const msg = errorMessage(e);
    expect(msg.title).toBe('Invalid EPCIS document');
    expect(msg.description).toContain('#2: eventTime required');
  });

  it('falls back to status text / plain body for non-problem errors', () => {
    expect(toApiError(502, 'Bad Gateway', undefined).title).toBe('Bad Gateway');
    expect(toApiError(500, '', 'boom').title).toBe('boom');
    expect(toApiError(404, '', { message: 'x' }).title).toBe('HTTP 404');
  });

  it('sends bearer token, JSON body and query string; calls onUnauthorized on 401', async () => {
    const onUnauthorized = vi.fn();
    const calls: { url: string; init: RequestInit }[] = [];
    const fetchImpl = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
      calls.push({ url: String(url), init: init ?? {} });
      return calls.length === 1 ? jsonResponse({ items: [{ id: 1 }], total: 7 }) : jsonResponse({ title: 'Unauthorized', status: 401 }, 401);
    }) as unknown as typeof fetch;
    const client = createApiClient({ fetchImpl, getToken: () => 'tok', onUnauthorized });
    const page = await client.list<{ id: number }>('/audit', { limit: 10, entity: '', action: undefined });
    expect(page).toEqual({ items: [{ id: 1 }], total: 7 });
    expect(calls[0].url).toBe('/api/v1/audit?limit=10');
    expect((calls[0].init.headers as Record<string, string>).Authorization).toBe('Bearer tok');
    await expect(client.post('/x', { a: 1 })).rejects.toBeInstanceOf(ApiError);
    expect(calls[1].init.body).toBe('{"a":1}');
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it('maps network failures to status 0', async () => {
    const client = createApiClient({ fetchImpl: vi.fn(async () => Promise.reject(new TypeError('Failed to fetch'))) as unknown as typeof fetch });
    const err = (await client.get('/meta').catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(0);
    expect(err.title).toBe('Network error');
  });

  it('normalises lists and query strings', () => {
    expect(toPage([1, 2])).toEqual({ items: [1, 2], total: 2 });
    expect(toPage(null)).toEqual({ items: [], total: 0 });
    expect(buildQuery({ a: ['x', 'y'], b: null })).toBe('?a=x&a=y');
  });
});
