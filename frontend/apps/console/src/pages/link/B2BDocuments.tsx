import { useState } from 'react';
import { Drawer, Segmented, Spin } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, CodeView, prettyJson, ProFormSelect, ProFormTextArea, type B2BDocument } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { usePartnerOptions } from '../../lib/options';

const TYPES = ['order', 'asn', 'invoice', 'inventory', 'return', 'chargeback'];
type Format = 'x12' | 'edifact' | 'idoc';

function EdiDrawer({ doc, onClose }: { doc: B2BDocument | null; onClose: () => void }) {
  const [format, setFormat] = useState<Format>('x12');
  const q = useQuery({
    queryKey: ['/b2b/documents', doc?.id, 'edi', format],
    queryFn: () => api.get<string>(`/b2b/documents/${doc!.id}/edi`, { format }, { responseType: 'text' }),
    enabled: !!doc,
  });
  return (
    <Drawer open={!!doc} onClose={onClose} title={`EDI · ${doc?.type ?? ''} · ${doc?.id ?? ''}`} size="large">
      <Segmented value={format} onChange={(v) => setFormat(v as Format)} options={['x12', 'edifact', 'idoc']} style={{ marginBottom: 12 }} />
      {q.isLoading ? <Spin /> : <CodeView code={typeof q.data === 'string' ? q.data : prettyJson(q.data)} filename={`${doc?.id}.${format}.txt`} maxHeight="60vh" />}
      <div style={{ marginTop: 16 }}>
        <CodeView title="Payload" code={prettyJson(doc?.payload ?? {})} maxHeight={300} />
      </div>
    </Drawer>
  );
}

export default function B2BDocuments() {
  const { t } = useTranslation();
  const partners = usePartnerOptions();
  const [view, setView] = useState<B2BDocument | null>(null);
  return (
    <>
      <CrudPage<B2BDocument & Record<string, unknown>>
        title={t('nav.b2bDocuments')}
        path="/b2b/documents"
        table={{ search: { labelWidth: 'auto' } }}
        columns={[
          { title: t('field.id'), dataIndex: 'id', copyable: true, ellipsis: true, search: false },
          { title: t('field.type'), dataIndex: 'type', valueType: 'select', valueEnum: Object.fromEntries(TYPES.map((x) => [x, { text: x }])) },
          { title: t('field.partner'), dataIndex: 'partnerId', search: false, render: (_d, r) => partners.find((p) => p.value === r.partnerId)?.label ?? r.partnerId ?? '–' },
          { title: t('field.status'), dataIndex: 'status', search: false },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime', search: false },
        ]}
        rowActions={(r) => [<a key="edi" onClick={() => setView(r)}>EDI</a>]}
        create={{
          width: 720,
          fields: (
            <>
              <ProFormSelect name="type" label={t('field.type')} options={TYPES} rules={[{ required: true }]} />
              <ProFormSelect name="partnerId" label={t('field.partner')} options={partners} rules={[{ required: true }]} />
              <ProFormTextArea
                name="payload"
                label="Payload (JSON)"
                rules={[{ required: true }, { validator: async (_r, v) => { if (v) JSON.parse(v as string); } }]}
                fieldProps={{ rows: 10, style: { fontFamily: 'monospace' } }}
              />
            </>
          ),
          transform: (v) => ({ ...v, payload: JSON.parse(v.payload as string) }),
        }}
      />
      <EdiDrawer doc={view} onClose={() => setView(null)} />
    </>
  );
}
