import { RedoOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { api, CodeView, PageContainer, prettyJson, QueryTable, StatusTag, type Report } from '@openspoor/ui/pro';
import { useAction } from '../../lib/hooks';

type Row = Report & Record<string, unknown>;

export default function Reports() {
  const { t } = useTranslation();
  const resubmit = useAction((id: string) => api.post(`/reports/${id}/resubmit`), { invalidate: [['/reports'], ['/alerts']] });
  return (
    <PageContainer title={t('nav.reports')}>
      <QueryTable<Row>
        rowKey="id"
        queryKey={['/reports']}
        search={{ labelWidth: 'auto' }}
        fetcher={async (p) => {
          const page = await api.list<Row>('/reports');
          const items = page.items.filter((r) => (!p.connector || r.connector === p.connector) && (!p.status || r.status === p.status));
          return { items, total: items.length };
        }}
        expandable={{ expandedRowRender: (r) => <CodeView code={prettyJson(r.response ?? {})} maxHeight={300} /> }}
        columns={[
          { title: t('field.id'), dataIndex: 'id', copyable: true, ellipsis: true, search: false },
          { title: 'Connector', dataIndex: 'connector' },
          { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
          { title: 'Attempts', dataIndex: 'attempts', search: false },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime', search: false },
          {
            title: t('common.actions'),
            valueType: 'option',
            render: (_d, r) => [
              <a key="r" onClick={() => resubmit.mutate(r.id)}>
                <RedoOutlined /> {t('action.resubmit')}
              </a>,
            ],
          },
        ]}
      />
    </PageContainer>
  );
}
