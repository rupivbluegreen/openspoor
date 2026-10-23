import { useEffect, useState } from 'react';
import { Alert, Button, Col, Modal, Row as GridRow, Segmented, Space, Statistic, Tag } from 'antd';
import { CloudUploadOutlined, PlusOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import dayjs from 'dayjs';
import { useSearchParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, gtinValidator, ModalForm, notifyError, PageContainer, prettyJson, ProForm, ProFormDatePicker, ProFormDigit, ProFormList,
  ProFormSelect, ProFormText, ProFormTextArea, QueryTable, StatusTag, type ReconcileResult, type Shipment,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import { usePartnerOptions } from '../../lib/options';

type Row = Shipment & Record<string, unknown>;
type Output = { title: string; body: React.ReactNode } | null;

function ReconcileView({ r }: { r: ReconcileResult }) {
  const matched = Array.isArray(r.matched) ? r.matched.length : typeof r.matched === 'number' ? r.matched : r.matched ? 'yes' : 'no';
  return (
    <>
      <GridRow gutter={16} style={{ marginBottom: 12 }}>
        <Col span={8}><Statistic title="Matched" value={matched} /></Col>
        <Col span={8}><Statistic title="Missing" value={r.missing?.length ?? 0} valueStyle={{ color: r.missing?.length ? '#C0392B' : undefined }} /></Col>
        <Col span={8}><Statistic title="Unexpected" value={r.unexpected?.length ?? 0} valueStyle={{ color: r.unexpected?.length ? '#C98A0B' : undefined }} /></Col>
      </GridRow>
      {!!r.missing?.length && <Alert type="error" title="Missing" description={r.missing.map((x) => <Tag key={x}>{x}</Tag>)} style={{ marginBottom: 8 }} />}
      {!!r.unexpected?.length && <Alert type="warning" title="Unexpected" description={r.unexpected.map((x) => <Tag key={x}>{x}</Tag>)} />}
    </>
  );
}

export default function Shipments() {
  const { t } = useTranslation();
  const partners = usePartnerOptions();
  const [direction, setDirection] = useState<'outbound' | 'inbound'>('outbound');
  const [output, setOutput] = useState<Output>(null);
  const [busy, setBusy] = useState<string | null>(null);
  // `?id=` (global search links): switch to the shipment's direction, then highlight its row.
  const focusId = useSearchParams()[0].get('id');
  const focused = useQuery({ queryKey: ['/shipments', focusId], queryFn: () => api.get<Shipment>(`/shipments/${focusId}`), enabled: !!focusId });
  const focusDirection = focused.data?.direction;
  useEffect(() => {
    if (focusDirection === 'inbound' || focusDirection === 'outbound') setDirection(focusDirection);
  }, [focusDirection]);

  const run = async (key: string, fn: () => Promise<unknown>, show: (r: unknown) => Output) => {
    setBusy(key);
    try {
      setOutput(show(await fn()));
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy(null);
    }
  };

  const create = useSubmit(
    (v: Record<string, unknown>) => api.post('/shipments', { ...v, shipDate: v.shipDate ? dayjs(v.shipDate as string).format('YYYY-MM-DD') : undefined }),
    { invalidate: [['/shipments']] },
  );
  const receive = useSubmit(
    (v: { document: string; asn?: string }) => api.post('/shipments/inbound', { document: v.document, asn: v.asn ? JSON.parse(v.asn) : undefined }),
    { invalidate: [['/shipments']] },
  );

  return (
    <PageContainer title={t('nav.shipments')} subTitle="DSCSA TI/TS">
      <Segmented
        value={direction}
        onChange={(v) => setDirection(v as typeof direction)}
        options={[
          { value: 'outbound', label: 'Outbound' },
          { value: 'inbound', label: 'Inbound' },
        ]}
        style={{ marginBottom: 16 }}
      />
      <QueryTable<Row>
        rowKey="id"
        queryKey={['/shipments']}
        tableId="shipments"
        focus={focusId ? { id: focusId } : undefined}
        // The contract does not define a direction filter, so filter client-side.
        extraParams={{ direction }}
        fetcher={async () => {
          const page = await api.list<Row>('/shipments');
          const items = page.items.filter((x) => !x.direction || x.direction === direction);
          return { items, total: items.length };
        }}
        columns={[
          { title: 'PO', dataIndex: 'poNumber', copyable: true },
          { title: t('field.partner'), dataIndex: 'partnerId', render: (_d, r) => partners.find((p) => p.value === r.partnerId)?.label ?? r.partnerId ?? '–',
            exportValue: (r: Row) => partners.find((p) => p.value === r.partnerId)?.label ?? r.partnerId ?? '' },
          { title: 'Ship date', dataIndex: 'shipDate', valueType: 'date' },
          { title: 'Items', dataIndex: 'items', render: (_d, r) => r.items?.length ?? 0, exportValue: (r: Row) => r.items?.length ?? 0 },
          { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
          {
            title: t('common.actions'),
            valueType: 'option',
            render: (_d, r) =>
              direction === 'outbound'
                ? [
                    <a key="tits" onClick={() => run(`tits-${r.id}`, () => api.post(`/shipments/${r.id}/tits`), (doc) => ({ title: 'TI/TS EPCIS document', body: <CodeView code={prettyJson(doc)} filename={`tits-${r.poNumber ?? r.id}.json`} /> }))}>
                      {busy === `tits-${r.id}` ? '…' : `${t('action.build')} TI/TS`}
                    </a>,
                    <a key="send" onClick={() => run(`send-${r.id}`, () => api.post(`/shipments/${r.id}/send`), (res) => ({ title: t('action.send'), body: <CodeView code={prettyJson(res ?? { status: 'sent' })} /> }))}>
                      {busy === `send-${r.id}` ? '…' : t('action.send')}
                    </a>,
                  ]
                : [
                    <a key="rec" onClick={() => run(`rec-${r.id}`, () => api.post<ReconcileResult>(`/shipments/${r.id}/reconcile`), (res) => ({ title: t('action.reconcile'), body: <ReconcileView r={(res ?? {}) as ReconcileResult} /> }))}>
                      {busy === `rec-${r.id}` ? '…' : t('action.reconcile')}
                    </a>,
                  ],
          },
        ]}
        toolBarRender={() => [
          <ModalForm<{ document: string; asn?: string }>
            key="inbound"
            title="Receive TI/TS"
            width={720}
            trigger={<Button icon={<CloudUploadOutlined />}>Receive TI/TS</Button>}
            modalProps={{ destroyOnHidden: true }}
            onFinish={receive}
          >
            <ProFormTextArea name="document" label="EPCIS document (TI/TS)" rules={[{ required: true }]} fieldProps={{ rows: 10, style: { fontFamily: 'monospace' } }} />
            <ProFormTextArea name="asn" label="ASN (JSON)" rules={[{ validator: async (_r, v) => { if (v) JSON.parse(v as string); } }]} fieldProps={{ rows: 5, style: { fontFamily: 'monospace' } }} />
          </ModalForm>,
          <ModalForm<Record<string, unknown>>
            key="create"
            title={t('nav.shipments')}
            width={720}
            trigger={<Button type="primary" icon={<PlusOutlined />}>{t('action.create')}</Button>}
            modalProps={{ destroyOnHidden: true }}
            initialValues={{ direction, items: [{}] }}
            onFinish={create}
          >
            <ProFormSelect name="direction" label={t('field.direction')} options={['outbound', 'inbound'].map((d) => ({ value: d, label: d }))} rules={[{ required: true }]} />
            <ProFormSelect name="partnerId" label={t('field.partner')} options={partners} rules={[{ required: true }]} showSearch />
            <ProFormText name="poNumber" label="PO number" />
            <ProFormDatePicker name="shipDate" label="Ship date" />
            <ProFormList name="items" label="Items" creatorButtonProps={{ creatorButtonText: t('action.add') }}>
              <ProForm.Group>
                <ProFormText name="gtin" label="GTIN" width="sm" rules={[gtinValidator(t('common.invalidGtin'))]} />
                <ProFormText name="lot" label={t('field.lot')} width="xs" />
                <ProFormDigit name="quantity" label={t('field.quantity')} width="xs" min={1} />
                <ProFormText name="epc" label="EPC / SSCC" width="sm" />
              </ProForm.Group>
            </ProFormList>
          </ModalForm>,
        ]}
      />
      <Modal open={!!output} title={output?.title} onCancel={() => setOutput(null)} footer={null} width={760}>
        <Space direction="vertical" style={{ width: '100%' }}>{output?.body}</Space>
      </Modal>
    </PageContainer>
  );
}
