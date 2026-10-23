import { useMemo, useState } from 'react';
import { Alert, Button, Card, Collapse, Space, Tag, Typography } from 'antd';
import { DownloadOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useSearchParams } from 'react-router';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import {
  api, downloadBlob, ExportCsvButton, ModalForm, notifyError, PageContainer, ProForm, ProFormDateRangePicker, ProFormSelect, ProFormText,
  ProFormTextArea, QueryTable, type AuthorityRequest, type FoodLot, type OneUpReport, type OneUpRow, type ProColumns,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import { useProductOptions } from '../../lib/options';

type Filters = { tlc?: string[]; productId?: string; from?: string; to?: string };

const REPORT = '/food/one-up-one-down';
const REQUESTS = '/food/one-up-one-down/authority-requests';

/** Fetches a server export (CSV or JSON, audited server-side) and saves it. */
async function download(path: string, query: Record<string, unknown>, format: 'csv' | 'json', name: string) {
  try {
    const body = await api.get<string>(path, { ...query, format }, { responseType: 'text' });
    downloadBlob(body, `${name}.${format}`, format === 'csv' ? 'text/csv;charset=utf-8' : 'application/json');
  } catch (e) {
    notifyError(e);
  }
}

function ExportButtons({ path, query, name, count }: { path: string; query: Record<string, unknown>; name: string; count: number }) {
  const { t } = useTranslation();
  return (
    <Space>
      <ExportCsvButton count={count} onExport={() => void download(path, query, 'csv', name)} />
      <Button icon={<DownloadOutlined />} onClick={() => void download(path, query, 'json', name)}>
        {t('oneUp.exportJson')}
      </Button>
    </Space>
  );
}

function AuthorityRequests() {
  const { t } = useTranslation();
  const columns: ProColumns<AuthorityRequest & Record<string, unknown>>[] = [
    { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
    { title: t('oneUp.authority'), dataIndex: 'authority' },
    { title: t('oneUp.reference'), dataIndex: 'reference', copyable: true },
    { title: t('oneUp.lots'), dataIndex: 'tlcs', render: (_d, r) => r.tlcs.map((l) => <Tag key={l}>{l}</Tag>) },
    { title: t('oneUp.period'), render: (_d, r) => `${r.periodFrom?.slice(0, 10) ?? '…'} – ${r.periodTo?.slice(0, 10) ?? '…'}` },
    { title: t('oneUp.rows'), dataIndex: 'rows' },
    { title: t('oneUp.createdBy'), dataIndex: 'createdBy' },
    { title: t('oneUp.downloads'), render: (_d, r) => r.exports?.length ?? 0 },
    { title: 'SHA-256', dataIndex: 'sha256', copyable: true, ellipsis: true, width: 140 },
    {
      title: t('common.actions'),
      valueType: 'option',
      render: (_d, r) => [
        <a key="csv" onClick={() => void download(`${REQUESTS}/${r.id}/export`, {}, 'csv', `one-up-one-down-${r.reference || r.id}`)}>CSV</a>,
        <a key="json" onClick={() => void download(`${REQUESTS}/${r.id}/export`, {}, 'json', `one-up-one-down-${r.reference || r.id}`)}>JSON</a>,
      ],
    },
  ];
  return (
    <QueryTable<AuthorityRequest & Record<string, unknown>>
      headerTitle={t('oneUp.authorityRequests')}
      rowKey="id"
      queryKey={[REQUESTS]}
      fetcher={() => api.list<AuthorityRequest & Record<string, unknown>>(REQUESTS)}
      columns={columns}
      scroll={{ x: 'max-content' }}
    />
  );
}

export default function OneUpOneDown() {
  const { t } = useTranslation();
  const [search] = useSearchParams();
  const initialLot = search.get('tlc');
  const [filters, setFilters] = useState<Filters>(initialLot ? { tlc: [initialLot] } : {});
  const products = useProductOptions();
  const lots = useQuery({ queryKey: ['/food/lots'], queryFn: () => api.list<FoodLot & Record<string, unknown>>('/food/lots'), meta: { silent: true } });
  const lotOptions = (lots.data?.items ?? []).map((l) => ({ value: l.tlc, label: l.tlc }));
  const qc = useQueryClient();
  const reportQuery = { queryKey: [REPORT, filters], queryFn: () => api.get<OneUpReport>(REPORT, filters) };
  const report = useQuery(reportQuery);
  const query = useMemo(() => ({ ...filters }), [filters]);
  const createRequest = useSubmit((v: { authority: string; reference?: string; reason?: string }) => api.post<AuthorityRequest>(REQUESTS, { ...v, tlcs: filters.tlc ?? [], from: filters.from, to: filters.to }), {
    invalidate: [[REQUESTS]],
    success: t('oneUp.assembled'),
  });

  const relation = (r: OneUpRow) => t(`oneUp.relation_${r.relation}`);
  const columns: (ProColumns<OneUpRow & Record<string, unknown>> & { exportValue?: (r: OneUpRow) => unknown })[] = [
    { title: t('field.lot'), dataIndex: 'lot', render: (_d, r) => <Link to={`/food/lots/${encodeURIComponent(r.lot)}`}>{r.lot}</Link> },
    {
      title: t('field.direction'),
      dataIndex: 'direction',
      render: (_d, r) => <Tag color={r.direction === 'one_up' ? 'blue' : 'green'}>{r.direction === 'one_up' ? t('oneUp.oneUp') : t('oneUp.oneDown')}</Tag>,
    },
    { title: t('field.type'), dataIndex: 'relation', render: (_d, r) => relation(r) },
    { title: t('oneUp.viaLot'), dataIndex: 'viaLot', render: (_d, r) => r.viaLot || '–' },
    {
      title: t('oneUp.counterparty'),
      dataIndex: 'counterpartyName',
      render: (_d, r) => (
        <Space direction="vertical" size={0}>
          <span>{r.counterpartyName || '–'}</span>
          {r.counterpartyId && <Typography.Text type="secondary" copyable>{r.counterpartyId}</Typography.Text>}
        </Space>
      ),
    },
    { title: t('oneUp.date'), dataIndex: 'eventTime', valueType: 'dateTime' },
    { title: t('oneUp.lotCode'), dataIndex: 'lotCode', copyable: true },
    { title: t('field.description'), dataIndex: 'productDescription' },
    { title: t('field.quantity'), dataIndex: 'quantity', render: (_d, r) => `${r.quantity} ${r.uom}` },
    { title: t('oneUp.refDoc'), dataIndex: 'refDocNumber', render: (_d, r) => [r.refDocType, r.refDocNumber].filter(Boolean).join(' ') || '–' },
    { title: t('oneUp.certificates'), dataIndex: 'certificates', render: (_d, r) => r.certificates?.map((c) => <Tag key={c}>{c}</Tag>) },
  ];

  return (
    <PageContainer title={t('nav.oneUpOneDown')} subTitle={t('oneUp.subtitle')}>
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Card>
          <ProForm<{ tlc?: string[]; productId?: string; range?: [string, string] }>
            layout="inline"
            initialValues={initialLot ? { tlc: [initialLot] } : undefined}
            submitter={{ searchConfig: { submitText: t('action.query') }, resetButtonProps: false }}
            onFinish={async (v) => {
              setFilters({
                tlc: v.tlc?.length ? v.tlc : undefined,
                productId: v.productId,
                from: v.range?.[0] ? dayjs(v.range[0]).format('YYYY-MM-DD') : undefined,
                to: v.range?.[1] ? dayjs(v.range[1]).format('YYYY-MM-DD') : undefined,
              });
              return true;
            }}
          >
            <ProFormSelect
              name="tlc"
              label={t('oneUp.lots')}
              mode="multiple"
              showSearch
              options={lotOptions}
              placeholder={t('oneUp.lotsPlaceholder')}
              fieldProps={{ style: { minWidth: 260 } }}
            />
            <ProFormSelect name="productId" label={t('field.product')} options={products} showSearch allowClear fieldProps={{ style: { minWidth: 200 } }} />
            <ProFormDateRangePicker name="range" label={t('oneUp.period')} />
          </ProForm>
        </Card>
        <Alert
          type="info"
          showIcon
          title={t('oneUp.formatNote')}
          description={
            report.data?.notes?.length ? (
              <Collapse
                ghost
                size="small"
                items={[{ key: 'notes', label: t('common.details'), children: report.data.notes.map((n) => <Typography.Paragraph key={n}>{n}</Typography.Paragraph>) }]}
              />
            ) : undefined
          }
        />
        {report.data?.truncated && <Alert type="warning" showIcon title={t('oneUp.truncated')} />}
        <QueryTable<OneUpRow & Record<string, unknown>>
          rowKey={(r) => `${r.lot}-${r.direction}-${r.eventId}-${r.lotCode}`}
          queryKey={[REPORT, 'rows']}
          extraParams={filters}
          fetcher={async () => {
            // Shares the cache entry with the notes above: one request per filter change.
            const rep = await qc.fetchQuery(reportQuery);
            return { items: rep.rows as (OneUpRow & Record<string, unknown>)[], total: rep.rows.length };
          }}
          columns={columns}
          scroll={{ x: 'max-content' }}
          toolBarRender={() => [
            <ExportButtons key="export" path={REPORT} query={query} name={`one-up-one-down-${dayjs().format('YYYYMMDD-HHmm')}`} count={report.data?.rows.length ?? 0} />,
            <ModalForm<{ authority: string; reference?: string; reason?: string }>
              key="authority"
              title={t('oneUp.newRequest')}
              trigger={<Button type="primary" disabled={!filters.tlc?.length} title={filters.tlc?.length ? undefined : t('oneUp.noLots')}>{t('oneUp.newRequest')}</Button>}
              modalProps={{ destroyOnHidden: true }}
              onFinish={createRequest}
            >
              <Alert type="info" showIcon style={{ marginBottom: 16 }} title={t('common.audited')} />
              <ProFormText name="authority" label={t('oneUp.authority')} rules={[{ required: true }]} />
              <ProFormText name="reference" label={t('oneUp.reference')} />
              <ProFormTextArea name="reason" label={t('field.reason')} />
              <Typography.Text type="secondary">
                {t('oneUp.lots')}: {filters.tlc?.join(', ')} · {t('oneUp.period')}: {filters.from ?? '…'} – {filters.to ?? '…'}
              </Typography.Text>
            </ModalForm>,
          ]}
        />
        <AuthorityRequests />
      </Space>
    </PageContainer>
  );
}
