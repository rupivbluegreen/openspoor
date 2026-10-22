/** RFC 9457 problem document as returned by the Open Spoor API. */
export interface Problem {
  type?: string;
  title: string;
  status: number;
  detail?: string;
  errors?: unknown;
}

export class ApiError extends Error {
  readonly status: number;
  readonly title: string;
  readonly detail?: string;
  readonly errors: unknown[];
  readonly problem?: Problem;

  constructor(status: number, title: string, opts: { detail?: string; errors?: unknown; problem?: Problem } = {}) {
    super(opts.detail ? `${title}: ${opts.detail}` : title);
    this.name = 'ApiError';
    this.status = status;
    this.title = title;
    this.detail = opts.detail;
    this.errors = Array.isArray(opts.errors) ? opts.errors : opts.errors === undefined || opts.errors === null ? [] : [opts.errors];
    this.problem = opts.problem;
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** Builds an ApiError from a non-2xx response body (problem+json, JSON or plain text). */
export function toApiError(status: number, statusText: string, body: unknown): ApiError {
  if (isRecord(body) && typeof body.title === 'string') {
    const problem: Problem = {
      type: typeof body.type === 'string' ? body.type : undefined,
      title: body.title,
      status: typeof body.status === 'number' ? body.status : status,
      detail: typeof body.detail === 'string' ? body.detail : undefined,
      errors: body.errors,
    };
    return new ApiError(problem.status, problem.title, { detail: problem.detail, errors: problem.errors, problem });
  }
  if (typeof body === 'string' && body.trim() !== '') {
    return new ApiError(status, body.trim().slice(0, 300));
  }
  return new ApiError(status, statusText || `HTTP ${status}`);
}

/** Renders one entry of a problem's `errors` array as text. */
export function formatProblemError(e: unknown): string {
  if (typeof e === 'string') return e;
  if (isRecord(e)) {
    const where = [e.index !== undefined ? `#${String(e.index)}` : '', typeof e.field === 'string' ? e.field : '', typeof e.path === 'string' ? e.path : '']
      .filter(Boolean)
      .join(' ');
    const msg = typeof e.message === 'string' ? e.message : typeof e.error === 'string' ? e.error : JSON.stringify(e);
    return where ? `${where}: ${msg}` : msg;
  }
  return String(e);
}

/** Human readable message for any thrown value (used for notifications). */
export function errorMessage(err: unknown): { title: string; description?: string } {
  if (err instanceof ApiError) {
    const lines = err.errors.slice(0, 5).map(formatProblemError);
    const description = [err.detail, ...lines].filter(Boolean).join('\n');
    return { title: err.title, description: description || undefined };
  }
  if (err instanceof Error) return { title: err.message };
  return { title: String(err) };
}
