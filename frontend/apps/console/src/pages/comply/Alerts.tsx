import { useState } from 'react';
import { Popconfirm, Segmented, Select, Space } from 'antd';
import { CheckOutlined, RedoOutlined, SecurityScanOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, CodeView, PageContainer, prettyJson, QueryTable, StatusTag, type Alert } from '@openspoor/ui/pro';
import { useAction } from '../../lib/hooks';

type Row = Alert & Record<string, unknown>;
const KINDS = ['regulatory', 'serialization', 'epcis', 'dscsa', 'vrs', 'agent', 'supplier', 'license', 'food'];

export default function Alerts() {
  const { t } = useTranslation();
  const [status, setStatus] = useState('open');
  const [kind, setKind] = useState<string | undefined>();
  const resolve = useAction((id: string) => api.post(`/alerts/${id}/resolve`), { invalidate: [['/alerts']] });
  const resubmit = useAction((reportId: string) => api.post(`/reports/${reportId}/resubmit`), { invalidate: [['/alerts'], ['/reports']] });
  const navigate = useNavigate();
  const investigate = useAction((alertId: string) => api.post<{ id: string }>('/investigations/from-alert', { alertId }), {
    invalidate: [['/alerts'], ['/investigations']],
    onSuccess: () => navigate('/comply/investigations'),
  });
  const investigable = (r: Row) => r.status === 'open' && (r.kind.startsWith('verify.') || r.kind === 'dscsa.vrs_not_verified');

  return (
    <PageContainer
      title={t('nav.alerts')}
      extra={
        <Space wrap>
          <Select allowClear placeholder={t('field.kind')} style={{ minWidth: 160 }} value={kind} onChange={setKind} options={KINDS.map((k) => ({ value: k, label: k }))} />
          <Segmented
            value={status}
            onChange={(v) => setStatus(String(v))}
            options={[
              { value: 'open', label: t('common.open') },
              { value: 'resolved', label: t('common.resolved') },
              { value: 'all', label: t('common.all') },
            ]}
          />
        </Space>
      }
    >
      <QueryTable<Row>
        rowKey="id"
        queryKey={['/alerts']}
        extraParams={{ status, kind }}
        fetcher={() => api.list<Row>('/alerts', { status, kind })}
        expandable={{ expandedRowRender: (r) => <CodeView code={prettyJson(r.detail ?? {})} maxHeight={260} />, rowExpandable: (r) => !!r.detail }}
        columns={[
          { title: t('field.severity'), dataIndex: 'severity', width: 110, render: (_d, r) => <StatusTag status={r.severity} /> },
          { title: t('field.title'), dataIndex: 'title' },
          { title: t('field.kind'), dataIndex: 'kind' },
          { title: t('field.entity'), render: (_d, r) => (r.entity ? `${r.entity}:${r.entityId ?? ''}` : '–'), ellipsis: true },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
          { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
          {
            title: t('common.actions'),
            valueType: 'option',
            render: (_d, r) => [
              investigable(r) ? (
                <Popconfirm key="investigate" title={`${t('nav.investigations')}?`} description={t('common.audited')} onConfirm={() => investigate.mutateAsync(r.id)}>
                  <a>
                    <SecurityScanOutlined /> {t('nav.investigations')}
                  </a>
                </Popconfirm>
              ) : null,
              r.detail?.reportId && r.status === 'open' ? (
                <a key="resubmit" onClick={() => resubmit.mutate(String(r.detail!.reportId))}>
                  <RedoOutlined /> {t('action.resubmit')}
                </a>
              ) : null,
              r.status === 'open' ? (
                <Popconfirm key="resolve" title={`${t('action.resolve')}?`} description={t('common.audited')} onConfirm={() => resolve.mutateAsync(r.id)}>
                  <a>
                    <CheckOutlined /> {t('action.resolve')}
                  </a>
                </Popconfirm>
              ) : null,
            ],
          },
        ]}
      />
    </PageContainer>
  );
}
