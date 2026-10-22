import { useState } from 'react';
import { Card, Drawer, Empty, Tag } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, CodeView, prettyJson, ProDescriptions, ProFormSelect, ProFormText, ProFormTextArea, StatusTag, useESignature, type Recall } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';

function RecallDrawer({ id, onClose }: { id: string | null; onClose: () => void }) {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ['/food/recalls', id], queryFn: () => api.get<Recall>(`/food/recalls/${id}`), enabled: !!id });
  const r = q.data;
  return (
    <Drawer open={!!id} onClose={onClose} title={r?.title ?? id} size="large" loading={q.isLoading}>
      {r ? (
        <>
          <ProDescriptions<Recall>
            column={1}
            dataSource={r}
            columns={[
              { title: t('field.status'), dataIndex: 'status', render: (_d, x) => <StatusTag status={x.status} /> },
              { title: t('field.reason'), dataIndex: 'reason' },
              { title: 'TLCs', dataIndex: 'tlcs', render: (_d, x) => x.tlcs?.map((l) => <Tag key={l}>{l}</Tag>) },
              { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
            ]}
          />
          <Card size="small" title={`Affected lots (${r.affectedLots?.length ?? 0})`} style={{ marginTop: 16 }}>
            <CodeView code={prettyJson(r.affectedLots ?? [])} maxHeight={240} />
          </Card>
          <Card size="small" title={`Affected customers (${r.affectedCustomers?.length ?? 0})`} style={{ marginTop: 16 }}>
            <CodeView code={prettyJson(r.affectedCustomers ?? [])} maxHeight={240} />
          </Card>
        </>
      ) : (
        <Empty />
      )}
    </Drawer>
  );
}

export default function Recalls() {
  const { t } = useTranslation();
  const [open, setOpen] = useState<string | null>(null);
  const { requestSignature, holder } = useESignature();
  return (
    <>
      <CrudPage<Recall & Record<string, unknown>>
        title={t('nav.recalls')}
        path="/food/recalls"
        columns={[
          { title: t('field.title'), dataIndex: 'title', render: (_d, r) => <a onClick={() => setOpen(r.id)}>{r.title ?? r.id}</a> },
          { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
          { title: 'TLCs', dataIndex: 'tlcs', render: (_d, r) => r.tlcs?.length ?? 0 },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
        ]}
        rowActions={(r) => [
          <a key="view" onClick={() => setOpen(r.id)}>{t('action.view')}</a>,
          <a key="sign" onClick={() => void requestSignature({ entity: 'recall', entityId: r.id, defaultMeaning: 'approval' })}>{t('action.sign')}</a>,
        ]}
        create={{
          fields: (
            <>
              <ProFormText name="title" label={t('field.title')} rules={[{ required: true }]} />
              <ProFormSelect name="classification" label="Class" options={['I', 'II', 'III']} />
              <ProFormSelect name="tlcs" label="TLCs" mode="tags" rules={[{ required: true }]} />
              <ProFormTextArea name="reason" label={t('field.reason')} rules={[{ required: true }]} />
            </>
          ),
        }}
      />
      <RecallDrawer id={open} onClose={() => setOpen(null)} />
      {holder}
    </>
  );
}
