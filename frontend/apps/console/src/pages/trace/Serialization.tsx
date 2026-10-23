import { useState } from 'react';
import { Alert, Button, Card, Col, Descriptions, Image, Progress, Row, Tabs, Typography } from 'antd';
import { PlusOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, gtinValidator, idempotencyKey, ModalForm, notifyError, PageContainer, ProForm, ProFormDigit, ProFormSelect,
  ProFormText, QueryTable, type Allocation, type DecodeResult, type EncodeResult, type SerialPool,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import { MARKETS, useProductOptions } from '../../lib/options';

type Row = SerialPool & Record<string, unknown>;

function AllocateButton({ pool }: { pool: SerialPool }) {
  const { t } = useTranslation();
  // One Idempotency-Key per dialog session: retries / double submits never allocate twice.
  const [key, setKey] = useState(idempotencyKey);
  const [result, setResult] = useState<Allocation | null>(null);
  const submit = useSubmit(
    (v: { quantity: number }) => api.post<Allocation>(`/serial-pools/${pool.id}/allocate`, v, { headers: { 'Idempotency-Key': key } }),
    { invalidate: [['/serial-pools']], onSuccess: (r) => setResult(r) },
  );
  return (
    <ModalForm<{ quantity: number }>
      title={`${t('action.allocate')} · ${pool.market}`}
      trigger={<a>{t('action.allocate')}</a>}
      width={560}
      onOpenChange={(o) => {
        if (o) {
          setKey(idempotencyKey());
          setResult(null);
        }
      }}
      modalProps={{ destroyOnHidden: true }}
      submitter={result ? { render: () => null } : undefined}
      onFinish={async (v) => {
        await submit(v);
        return false;
      }}
    >
      {!result ? (
        <>
          <ProFormDigit name="quantity" label={t('field.quantity')} min={1} max={pool.remaining ?? 1_000_000} rules={[{ required: true }]} fieldProps={{ precision: 0 }} />
          <Typography.Text type="secondary">
            Idempotency-Key: <Typography.Text code>{key}</Typography.Text>
          </Typography.Text>
        </>
      ) : (
        <>
          <Alert type="success" showIcon title={`${result.count} serials · ${result.gtin}`} description={`allocationId ${result.allocationId}`} style={{ marginBottom: 12 }} />
          <CodeView code={(result.serials ?? []).join('\n')} filename={`serials-${result.allocationId}.txt`} maxHeight={280} />
        </>
      )}
    </ModalForm>
  );
}

function Pools() {
  const { t } = useTranslation();
  const products = useProductOptions();
  const create = useSubmit((v: Record<string, unknown>) => api.post('/serial-pools', v), { invalidate: [['/serial-pools']] });
  return (
    <QueryTable<Row>
      rowKey="id"
      queryKey={['/serial-pools']}
      tableId="serial-pools"
      fetcher={() => api.list<Row>('/serial-pools')}
      columns={[
        { title: t('field.product'), dataIndex: 'productId', render: (_d, r) => products.find((p) => p.value === r.productId)?.label ?? r.gtin ?? r.productId },
        { title: t('field.market'), dataIndex: 'market' },
        { title: 'Mode', dataIndex: 'mode' },
        { title: 'Length', dataIndex: 'length' },
        { title: 'Prefix', dataIndex: 'prefix' },
        { title: 'Issued', dataIndex: 'issued', valueType: 'digit' },
        { title: 'Remaining', dataIndex: 'remaining', valueType: 'digit' },
        {
          title: 'Capacity',
          dataIndex: 'capacity',
          render: (_d, r) => {
            const pct = r.capacity ? Math.round(((r.issued ?? 0) / r.capacity) * 100) : 0;
            const low = r.capacity && r.remaining !== undefined && r.lowWatermarkPct ? (r.remaining / r.capacity) * 100 < r.lowWatermarkPct : false;
            return <Progress percent={pct} size="small" status={low ? 'exception' : 'normal'} style={{ minWidth: 120 }} />;
          },
        },
        { title: t('common.actions'), valueType: 'option', render: (_d, r) => [<AllocateButton key="a" pool={r} />] },
      ]}
      toolBarRender={() => [
        <ModalForm<Record<string, unknown>>
          key="create"
          title="Serial pool"
          trigger={<Button type="primary" icon={<PlusOutlined />}>{t('action.create')}</Button>}
          modalProps={{ destroyOnHidden: true }}
          initialValues={{ mode: 'random', length: 12, lowWatermarkPct: 10, market: 'US' }}
          onFinish={create}
        >
          <ProFormSelect name="productId" label={t('field.product')} options={products} rules={[{ required: true }]} showSearch />
          <ProFormSelect name="market" label={t('field.market')} options={MARKETS} rules={[{ required: true }]} />
          <ProFormSelect name="mode" label="Mode" options={['random', 'sequential'].map((m) => ({ value: m, label: m }))} rules={[{ required: true }]} />
          <ProFormText name="alphabet" label="Alphabet" placeholder="0123456789ABCDEFGHJKLMNPQRSTUVWXYZ" tooltip="GS1 AI(21) allows up to 20 alphanumeric characters (CSET 82)" />
          <ProFormDigit name="length" label="Length" min={4} max={20} rules={[{ required: true }]} fieldProps={{ precision: 0 }} />
          <ProFormText name="prefix" label="Prefix" />
          <ProFormDigit name="lowWatermarkPct" label="Low watermark %" min={0} max={100} />
        </ModalForm>,
      ]}
    />
  );
}

function CodeTool() {
  const { t } = useTranslation();
  const [enc, setEnc] = useState<EncodeResult | null>(null);
  const [dec, setDec] = useState<DecodeResult | null>(null);
  return (
    <Row gutter={[16, 16]}>
      <Col xs={24} lg={12}>
        <Card title={`${t('action.encode')} (DataMatrix)`}>
          <ProForm<Record<string, string>>
            initialValues={{ format: 'png' }}
            submitter={{ searchConfig: { submitText: t('action.encode') }, resetButtonProps: false }}
            onFinish={async (v) => {
              try {
                setEnc(await api.post<EncodeResult>('/codes/encode', { ...v, lot: v.lot || undefined, expiry: v.expiry || undefined }));
              } catch (e) {
                notifyError(e);
              }
            }}
          >
            <ProFormText name="gtin" label="GTIN-14" rules={[{ required: true }, gtinValidator(t('common.invalidGtin'))]} />
            <ProFormText name="serial" label={t('field.serial')} rules={[{ required: true }, { max: 20 }]} />
            <ProFormText name="lot" label={t('field.lot')} rules={[{ max: 20 }]} />
            <ProFormText name="expiry" label={`${t('field.expiry')} (YYMMDD)`} rules={[{ pattern: /^\d{6}$/, message: 'YYMMDD' }]} />
            <ProFormSelect name="format" label="Format" options={['png', 'svg', 'text'].map((f) => ({ value: f, label: f }))} />
          </ProForm>
          {enc && (
            <>
              {enc.image && (
                <div style={{ textAlign: 'center', margin: '16px 0', background: '#fff', padding: 12, borderRadius: 6 }}>
                  <Image src={enc.image} alt="DataMatrix" width={180} style={{ imageRendering: 'pixelated' }} />
                </div>
              )}
              <Descriptions size="small" column={1} bordered items={[
                { key: 'hri', label: 'HRI', children: <Typography.Text copyable>{enc.hri}</Typography.Text> },
                { key: 'es', label: 'Element string', children: <Typography.Text copyable code>{enc.elementString}</Typography.Text> },
                { key: 'dl', label: 'Digital Link', children: <Typography.Text copyable>{enc.digitalLink}</Typography.Text> },
                { key: 'urn', label: 'EPC URN', children: <Typography.Text copyable>{enc.urn}</Typography.Text> },
                ...(enc.image ? [{ key: 'img', label: 'Data URI', children: <Typography.Text copyable={{ text: enc.image }} ellipsis style={{ maxWidth: 260 }}>{enc.image}</Typography.Text> }] : []),
              ]} />
            </>
          )}
        </Card>
      </Col>
      <Col xs={24} lg={12}>
        <Card title={t('action.decode')}>
          <ProForm<{ code: string }>
            submitter={{ searchConfig: { submitText: t('action.decode') }, resetButtonProps: false }}
            onFinish={async (v) => {
              try {
                setDec(await api.post<DecodeResult>('/codes/decode', v));
              } catch (e) {
                notifyError(e);
              }
            }}
          >
            <ProFormText name="code" label={t('field.code')} placeholder="(01)09506000134352(21)ABC123 or https://id.gs1.org/01/…" rules={[{ required: true }]} />
          </ProForm>
          {dec && (
            <Descriptions size="small" column={1} bordered items={[
              { key: 'gtin', label: 'GTIN', children: dec.gtin },
              { key: 'serial', label: t('field.serial'), children: dec.serial },
              { key: 'lot', label: t('field.lot'), children: dec.lot },
              { key: 'expiry', label: t('field.expiry'), children: dec.expiry },
              { key: 'urn', label: 'EPC URN', children: <Typography.Text copyable>{dec.urn}</Typography.Text> },
              { key: 'dl', label: 'Digital Link', children: <Typography.Text copyable>{dec.digitalLink}</Typography.Text> },
            ]} />
          )}
        </Card>
      </Col>
    </Row>
  );
}

export default function Serialization() {
  const { t } = useTranslation();
  return (
    <PageContainer title={t('nav.serialization')}>
      <Tabs
        items={[
          { key: 'pools', label: 'Serial pools', children: <Pools /> },
          { key: 'codes', label: <span><ThunderboltOutlined /> DataMatrix</span>, children: <CodeTool /> },
        ]}
      />
    </PageContainer>
  );
}
