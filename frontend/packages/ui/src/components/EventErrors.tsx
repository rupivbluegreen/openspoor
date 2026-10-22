import { Table } from 'antd';
import { useTranslation } from 'react-i18next';
import { formatProblemError } from '../api/errors';
import type { EventError } from '../api/types';

/** Per-event validation errors (strings or {index, eventId, field/path, message}). */
export function EventErrors({ errors }: { errors?: (EventError | string)[] | unknown[] }) {
  const { t } = useTranslation();
  const rows = (errors ?? []).map((e, i) =>
    typeof e === 'object' && e !== null ? { key: i, ...(e as EventError) } : { key: i, message: formatProblemError(e) },
  );
  return (
    <Table
      size="small"
      rowKey="key"
      dataSource={rows}
      pagination={rows.length > 20 ? { pageSize: 20 } : false}
      locale={{ emptyText: t('common.empty') }}
      columns={[
        { title: 'Event #', dataIndex: 'index', width: 90, render: (v) => (v === undefined ? '–' : String(v)) },
        { title: 'Event ID', dataIndex: 'eventId', ellipsis: true, render: (v) => (v ? String(v) : '–') },
        { title: 'Field', render: (_v, r) => String(r.field ?? r.path ?? '–') },
        { title: 'Message', dataIndex: 'message', render: (v, r) => String(v ?? formatProblemError(r)) },
      ]}
    />
  );
}
