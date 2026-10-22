import { Tag } from 'antd';

const COLORS: Record<string, string> = {
  verified: 'success', active: 'success', ok: 'success', healthy: 'success', approved: 'success', accepted: 'success',
  done: 'success', completed: 'success', succeeded: 'success', success: 'success', sent: 'success', matched: 'success',
  connected: 'success', valid: 'success', resolved: 'default', closed: 'default', revoked: 'default', disabled: 'default',
  pending: 'processing', queued: 'processing', processing: 'processing', running: 'processing', open: 'processing',
  invited: 'processing', draft: 'default', submitted: 'processing', in_progress: 'processing',
  warning: 'warning', degraded: 'warning', suspect: 'warning', expiring: 'warning', medium: 'warning', suspended: 'warning',
  failed: 'error', error: 'error', rejected: 'error', critical: 'error', high: 'error', recalled: 'error',
  expired: 'error', down: 'error', invalid: 'error', not_found: 'error', reverted: 'warning', low: 'default', info: 'blue',
};

export function statusColor(status?: string | null): string {
  return COLORS[String(status ?? '').toLowerCase().replace(/[\s-]/g, '_')] ?? 'default';
}

export function StatusTag({ status }: { status?: string | null }) {
  if (!status) return <>–</>;
  return <Tag color={statusColor(status)}>{status}</Tag>;
}
