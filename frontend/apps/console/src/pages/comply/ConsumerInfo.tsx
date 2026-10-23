import { useMemo, useState } from 'react';
import { AutoComplete, Button, Card, Col, Empty, Popconfirm, Result, Row, Segmented, Statistic, Tag, Typography } from 'antd';
import { DeleteOutlined, EditOutlined, LinkOutlined, PlusOutlined } from '@ant-design/icons';
import { Column } from '@ant-design/charts';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, ModalForm, PageContainer, ProForm, ProFormGroup, ProFormList, ProFormSelect, ProFormText, ProFormTextArea, QueryTable, useAppSettings,
  type ConsumerInfo, type ConsumerInfoInput, type ConsumerReport, type Product, type ProColumns, type VerifyStats,
} from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';
import { useAction, useSubmit } from '../../lib/hooks';

type Row = ConsumerInfo & Record<string, unknown>;
type ReportRow = ConsumerReport & Record<string, unknown>;

interface FormValues {
  gtin: string;
  lang: string;
  leafletUrl?: string;
  leafletText?: string;
  allergens?: string[];
  origin?: string;
  certificates?: { name?: string; issuer?: string; url?: string }[];
}

const INFO_KEY = ['/consumer-info'] as const;
const REPORTS_KEY = ['/verify/reports'] as const;
const OUTCOMES = ['verified', 'suspect', 'not_found', 'decommissioned', 'expired', 'recalled', 'reported'];
const httpsRule = (msg: string) => ({ pattern: /^https:\/\/\S+$/, message: msg });

function toBody(v: FormValues): ConsumerInfoInput {
  return {
    leafletUrl: v.leafletUrl?.trim() ?? '',
    leafletText: v.leafletText ?? '',
    allergens: (v.allergens ?? []).map((a) => a.trim()).filter(Boolean),
    origin: v.origin?.trim() ?? '',
    certificates: (v.certificates ?? [])
      .filter((c) => c.name?.trim())
      .map((c) => ({ name: c.name!.trim(), ...(c.issuer?.trim() ? { issuer: c.issuer.trim() } : {}), ...(c.url?.trim() ? { url: c.url.trim() } : {}) })),
  };
}

function useProducts() {
  const q = useQuery({ queryKey: ['/products'], queryFn: () => api.list<Product>('/products'), meta: { silent: true } });
  return useMemo(() => {
    const items = q.data?.items ?? [];
    return { names: new Map(items.map((p) => [p.gtin, p.name])), options: items.map((p) => ({ value: p.gtin, label: `${p.name} · ${p.gtin}` })) };
  }, [q.data]);
}

function InfoForm({ record, products, onDone }: { record?: Row; products: { value: string; label: string }[]; onDone: () => void }) {
  const { t } = useTranslation();
  const save = useSubmit(
    (v: FormValues) => api.put<ConsumerInfo>(`/consumer-info/${encodeURIComponent(v.gtin)}/${encodeURIComponent(v.lang)}`, toBody(v)),
    { invalidate: [INFO_KEY], success: t('common.saved') },
  );
  const initial: Partial<FormValues> = record
    ? { ...record, certificates: record.certificates ?? [], allergens: record.allergens ?? [] }
    : { lang: 'en', allergens: [], certificates: [] };
  return (
    <ModalForm<FormValues>
      open
      title={record ? `${t('action.edit')} · ${record.gtin} · ${record.lang}` : t('action.create')}
      width={680}
      modalProps={{ destroyOnHidden: true, onCancel: onDone }}
      initialValues={initial}
      onOpenChange={(o) => !o && onDone()}
      onFinish={async (v) => {
        const ok = await save({ ...v, gtin: record?.gtin ?? v.gtin, lang: record?.lang ?? v.lang });
        if (ok) onDone();
        return ok;
      }}
    >
      <ProFormGroup>
        <ProFormSelect
          name="gtin"
          label={t('field.product')}
          width="md"
          options={products}
          disabled={!!record}
          showSearch
          rules={[{ required: true }]}
        />
        <ProForm.Item
          name="lang"
          label={t('consumer.lang')}
          normalize={(v?: string) => v?.toLowerCase().trim()}
          rules={[{ required: true }, { pattern: /^[a-z]{2}$/, message: t('consumer.langInvalid') }]}
        >
          <AutoComplete
            disabled={!!record}
            style={{ width: 120 }}
            options={[{ value: 'en', label: 'en · English' }, { value: 'nl', label: 'nl · Nederlands' }, { value: 'ar', label: 'ar · العربية' }]}
          />
        </ProForm.Item>
      </ProFormGroup>
      <ProFormText name="leafletUrl" label={t('consumer.leafletUrl')} placeholder="https://" rules={[httpsRule(t('consumer.httpsOnly'))]} fieldProps={{ dir: 'ltr' }} />
      <ProFormTextArea name="leafletText" label={t('consumer.leafletText')} fieldProps={{ maxLength: 100_000, showCount: true, autoSize: { minRows: 4, maxRows: 12 } }} />
      <ProFormSelect name="allergens" label={t('consumer.allergens')} mode="tags" placeholder={t('consumer.allergensHint')} fieldProps={{ open: false, tokenSeparators: [','] }} />
      <ProFormText name="origin" label={t('consumer.origin')} />
      <ProFormList
        name="certificates"
        label={t('consumer.certificates')}
        creatorButtonProps={{ creatorButtonText: t('consumer.addCertificate') }}
        creatorRecord={{ name: '' }}
      >
        <ProFormGroup>
          <ProFormText name="name" label={t('consumer.certName')} width="sm" rules={[{ required: true }]} />
          <ProFormText name="issuer" label={t('consumer.certIssuer')} width="sm" />
          <ProFormText name="url" label={t('consumer.certUrl')} width="sm" placeholder="https://" rules={[httpsRule(t('consumer.httpsOnly'))]} fieldProps={{ dir: 'ltr' }} />
        </ProFormGroup>
      </ProFormList>
    </ModalForm>
  );
}

function ScanStats({ names }: { names: Map<string, string> }) {
  const { t } = useTranslation();
  const { mode } = useAppSettings();
  const [days, setDays] = useState(30);
  const stats = useQuery({ queryKey: ['/verify/stats', days], queryFn: () => api.get<VerifyStats>('/verify/stats', { days }) });
  const items = stats.data?.items ?? [];
  const label = (o: string) => (OUTCOMES.includes(o) || o === 'unknown_product' ? t(`consumer.outcome.${o}`) : o);
  const data = items
    .map((x) => ({ outcome: label(x.outcome), order: OUTCOMES.indexOf(x.outcome), product: names.get(x.gtin) ?? x.gtin, count: Number(x.count) }))
    .sort((a, b) => a.order - b.order);
  const total = (o?: string) => items.filter((x) => !o || x.outcome === o).reduce((n, x) => n + Number(x.count), 0);
  const scans = items.filter((x) => x.outcome !== 'reported').reduce((n, x) => n + Number(x.count), 0);
  return (
    <Card
      title={t('consumer.scans')}
      extra={<Segmented size="small" value={days} onChange={(v) => setDays(Number(v))} options={[7, 30, 90].map((n) => ({ value: n, label: t('consumer.lastDays', { n }) }))} />}
      loading={stats.isLoading}
    >
      <Typography.Paragraph type="secondary">{t('consumer.scansHint')}</Typography.Paragraph>
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col xs={12} md={6}><Statistic title={t('consumer.totalScans')} value={scans} /></Col>
        <Col xs={12} md={6}><Statistic title={t('consumer.outcome.verified')} value={total('verified')} styles={{ content: { color: '#2F8F46' } }} /></Col>
        <Col xs={12} md={6}><Statistic title={t('consumer.notVerified')} value={scans - total('verified')} styles={{ content: { color: '#C98A0B' } }} /></Col>
        <Col xs={12} md={6}><Statistic title={t('consumer.outcome.reported')} value={total('reported')} styles={{ content: { color: '#C0392B' } }} /></Col>
      </Row>
      {data.length ? (
        <Column data={data} xField="outcome" yField="count" colorField="product" stack height={300} theme={mode === 'dark' ? 'classicDark' : 'classic'} legend={{ color: { position: 'bottom' } }} />
      ) : (
        <Empty description={t('common.empty')} />
      )}
    </Card>
  );
}

export default function ConsumerInfoPage() {
  const { t } = useTranslation();
  const { hasRole, loading } = useSession();
  const { names, options } = useProducts();
  const [editing, setEditing] = useState<Row | 'new' | null>(null);
  const del = useAction((r: Row) => api.del(`/consumer-info/${encodeURIComponent(r.gtin)}/${encodeURIComponent(r.lang)}`), { invalidate: [INFO_KEY] });
  const allowed = hasRole('Compliance');

  if (!loading && !allowed) {
    return (
      <PageContainer title={t('nav.consumerInfo')}>
        <Result status="403" title="403" subTitle={t('consumer.noAccess')} />
      </PageContainer>
    );
  }

  const columns: ProColumns<Row>[] = [
    { title: 'GTIN', dataIndex: 'gtin', copyable: true },
    { title: t('field.product'), key: 'product', render: (_d, r) => names.get(r.gtin) ?? '–', exportValue: (r: Row) => names.get(r.gtin) ?? '' } as ProColumns<Row>,
    { title: t('consumer.lang'), dataIndex: 'lang', render: (_d, r) => <Tag>{r.lang}</Tag> },
    {
      title: t('consumer.leaflet'),
      key: 'leaflet',
      render: (_d, r) => (
        <>
          {r.leafletUrl ? <a href={r.leafletUrl} target="_blank" rel="noopener noreferrer"><LinkOutlined /> URL</a> : null}
          {r.leafletUrl && r.leafletText ? ' · ' : null}
          {r.leafletText ? <Typography.Text type="secondary">{t('consumer.leafletText')} ({r.leafletText.length.toLocaleString()})</Typography.Text> : null}
          {!r.leafletUrl && !r.leafletText ? '–' : null}
        </>
      ),
    },
    { title: t('consumer.allergens'), key: 'allergens', render: (_d, r) => r.allergens?.length ?? 0, exportValue: (r: Row) => (r.allergens ?? []).join('; ') } as ProColumns<Row>,
    { title: t('consumer.origin'), dataIndex: 'origin', ellipsis: true },
    { title: t('consumer.certificates'), key: 'certificates', render: (_d, r) => r.certificates?.length ?? 0 },
    { title: t('consumer.updated'), dataIndex: 'updatedAt', valueType: 'dateTime' },
    {
      title: t('common.actions'),
      valueType: 'option',
      fixed: 'right',
      width: 180,
      render: (_d, r) => [
        <a key="edit" onClick={() => setEditing(r)}><EditOutlined /> {t('action.edit')}</a>,
        <Popconfirm key="del" title={`${t('action.delete')} ${r.gtin} · ${r.lang}?`} description={t('common.audited')} onConfirm={() => del.mutateAsync(r)}>
          <a><DeleteOutlined /> {t('action.delete')}</a>
        </Popconfirm>,
      ],
    },
  ];

  const reportColumns: ProColumns<ReportRow>[] = [
    { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime', width: 170 },
    { title: t('field.product'), key: 'product', render: (_d, r) => names.get(r.gtin) ?? r.gtin, exportValue: (r: ReportRow) => names.get(r.gtin) ?? r.gtin } as ProColumns<ReportRow>,
    { title: 'GTIN', dataIndex: 'gtin', copyable: true },
    { title: t('field.serial'), dataIndex: 'serial', copyable: true },
    { title: t('field.lot'), dataIndex: 'lot' },
    {
      title: t('consumer.reason'),
      dataIndex: 'reason',
      render: (_d, r) => <Tag color="volcano">{['packaging', 'seal', 'effect', 'price', 'seller', 'other'].includes(r.reason) ? t(`consumerReason.${r.reason}`) : r.reason}</Tag>,
    },
    { title: t('consumer.details'), dataIndex: 'details', ellipsis: true, width: 280 },
    { title: t('field.country'), dataIndex: 'country', width: 90 },
  ];

  return (
    <PageContainer title={t('nav.consumerInfo')} subTitle={t('consumer.subtitle')}>
      <QueryTable<Row>
        rowKey={(r) => `${r.gtin}/${r.lang}`}
        queryKey={INFO_KEY}
        fetcher={() => api.list<Row>('/consumer-info')}
        columns={columns}
        tableId="consumer-info"
        search={false}
        scroll={{ x: 'max-content' }}
        toolBarRender={() => [
          <Button key="create" type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>{t('action.create')}</Button>,
        ]}
      />
      {editing && <InfoForm record={editing === 'new' ? undefined : editing} products={options} onDone={() => setEditing(null)} />}
      <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
        <Col span={24}>
          <ScanStats names={names} />
        </Col>
        <Col span={24}>
          <Card title={t('consumer.reports')} styles={{ body: { padding: 0 } }}>
            <Typography.Paragraph type="secondary" style={{ padding: '12px 24px 0', marginBottom: 0 }}>{t('consumer.reportsHint')}</Typography.Paragraph>
            <QueryTable<ReportRow>
              rowKey="id"
              queryKey={REPORTS_KEY}
              fetcher={() => api.list<ReportRow>('/verify/reports')}
              columns={reportColumns}
              tableId="verify-reports"
              search={false}
              scroll={{ x: 'max-content' }}
            />
          </Card>
        </Col>
      </Row>
    </PageContainer>
  );
}
