import { Popconfirm } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, ProFormSelect, ProFormText, ProFormTextArea, type Subscription } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { useAction } from '../../lib/hooks';

export default function Subscriptions() {
  const { t } = useTranslation();
  const named = useQuery({ queryKey: ['/epcis/queries'], queryFn: () => api.list<{ name: string }>('/epcis/queries'), meta: { silent: true } });
  const del = useAction((id: string) => api.del(`/epcis/subscriptions/${id}`), { invalidate: [['/epcis/subscriptions']] });
  return (
    <CrudPage<Subscription & Record<string, unknown>>
      title={t('nav.subscriptions')}
      subTitle="Webhooks signed with HMAC-SHA256 (X-OpenSpoor-Signature)"
      path="/epcis/subscriptions"
      columns={[
        { title: t('field.id'), dataIndex: 'id', copyable: true, ellipsis: true },
        { title: 'Query', dataIndex: 'queryName', render: (_d, r) => r.queryName ?? (r.query ? JSON.stringify(r.query) : '–') },
        { title: t('field.destination'), dataIndex: 'dest', copyable: true },
        { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
      ]}
      rowActions={(r) => [
        <Popconfirm key="del" title={`${t('action.delete')}?`} onConfirm={() => del.mutateAsync(r.id)}>
          <a>{t('action.delete')}</a>
        </Popconfirm>,
      ]}
      create={{
        fields: (
          <>
            <ProFormSelect name="queryName" label="Named query" options={(named.data?.items ?? []).map((n) => ({ value: n.name, label: n.name }))} allowClear />
            <ProFormTextArea name="query" label="Ad-hoc query (JSON)" tooltip="Used when no named query is selected" placeholder='{"eventType":["ObjectEvent"],"EQ_bizStep":["shipping"]}' rules={[{ validator: async (_r, v) => { if (v) JSON.parse(v as string); } }]} />
            <ProFormText name="dest" label={t('field.destination')} rules={[{ required: true }, { type: 'url' }]} placeholder="https://partner.example.com/epcis/hook" />
            <ProFormText.Password name="secret" label="HMAC secret" />
          </>
        ),
        transform: (v) => ({ queryName: v.queryName || undefined, query: v.query ? JSON.parse(v.query as string) : undefined, dest: v.dest, secret: v.secret || undefined }),
      }}
    />
  );
}
