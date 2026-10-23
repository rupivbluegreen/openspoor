import { useEffect, useState } from 'react';
import { Alert, Button, Checkbox, Empty, InputNumber, Space, Table, Tabs, Tag, Typography } from 'antd';
import { PlayCircleOutlined, SearchOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Input } from 'antd';
import {
  api, formatDateTime, PageContainer, StatusTag,
  type BalanceAnalysis, type BalanceReportRow, type CustodyGap, type CustodyGaps, type LotBalance, type TraceSettings,
} from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';
import { useAction } from '../../lib/hooks';

const SETTINGS = ['/trace/balance/settings'] as const;
const REPORTS = ['/trace/balance/reports'] as const;

/** Mass-balance reconciliation per lot and chain-of-custody gaps, from the stored event projections. */
export default function MassBalance() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canWrite = hasRole('Compliance');
  const [gtin, setGtin] = useState('');
  const [lot, setLot] = useState('');
  const [submitted, setSubmitted] = useState<{ gtin: string; lot: string } | null>(null);
  const q = { gtin: submitted?.gtin, lot: submitted?.lot };
  const balance = useQuery({ queryKey: ['/trace/balance', q], queryFn: () => api.get<BalanceAnalysis>('/trace/balance', q), enabled: !!submitted });
  const gaps = useQuery({ queryKey: ['/trace/balance/gaps', q], queryFn: () => api.get<CustodyGaps>('/trace/balance/gaps', q), enabled: !!submitted });
  const reports = useQuery({ queryKey: REPORTS, queryFn: () => api.list<BalanceReportRow>('/trace/balance/reports', { limit: 20 }) });
  const settings = useQuery({ queryKey: SETTINGS, queryFn: () => api.get<TraceSettings>('/trace/balance/settings') });
  const [form, setForm] = useState<TraceSettings | null>(null);
  useEffect(() => {
    if (settings.data) setForm(settings.data);
  }, [settings.data]);
  const run = useAction(() => api.post('/trace/balance/run', { gtin: submitted?.gtin, lot: submitted?.lot }), {
    invalidate: [REPORTS, ['/trace/balance'], ['/alerts']],
    success: t('massBalance.ran'),
  });
  const save = useAction((v: TraceSettings) => api.put('/trace/balance/settings', v), { invalidate: [SETTINGS, ['/trace/balance']], success: t('common.saved') });
  const allGaps: (CustodyGap & { key: string })[] = (gaps.data?.lots ?? []).flatMap((l) => l.gaps.map((g, i) => ({ ...g, key: `${l.lot}-${g.eventId}-${g.epc}-${i}` })));

  return (
    <PageContainer title={t('nav.massBalance')} subTitle={t('massBalance.subtitle')}>
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Alert type="info" showIcon message={t('massBalance.dataNote')} description={t('massBalance.helperNote')} />
        <Space wrap>
          <Input placeholder={t('massBalance.gtin')} value={gtin} onChange={(e) => setGtin(e.target.value.trim())} style={{ width: 200 }} />
          <Input placeholder={t('massBalance.lot')} value={lot} onChange={(e) => setLot(e.target.value.trim())} style={{ width: 160 }} />
          <Button type="primary" icon={<SearchOutlined />} disabled={!gtin} onClick={() => setSubmitted({ gtin, lot })}>{t('massBalance.analyse')}</Button>
          {canWrite && (
            <Button icon={<PlayCircleOutlined />} disabled={!submitted} loading={run.isPending} onClick={() => run.mutate(undefined)}>{t('massBalance.run')}</Button>
          )}
        </Space>
        {balance.data?.truncated && <Alert type="warning" showIcon message={t('massBalance.truncated')} />}
        <Tabs
          items={[
            {
              key: 'balance',
              label: t('massBalance.balance'),
              children: (
                <Table<LotBalance>
                  size="small"
                  rowKey="lot"
                  loading={balance.isLoading}
                  pagination={false}
                  locale={{ emptyText: <Empty description={submitted ? t('common.empty') : undefined} /> }}
                  dataSource={balance.data?.lots ?? []}
                  expandable={{
                    rowExpandable: (r) => r.anomalies.length > 0,
                    expandedRowRender: (r) => (
                      <Table
                        size="small"
                        rowKey="epc"
                        pagination={false}
                        title={() => t('massBalance.anomalies')}
                        dataSource={r.anomalies}
                        columns={[
                          { title: t('field.serial'), dataIndex: 'epc', render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
                          { title: 'In', dataIndex: 'in' },
                          { title: 'Out', dataIndex: 'out' },
                          { title: t('massBalance.detail'), dataIndex: 'issue' },
                        ]}
                      />
                    ),
                  }}
                  columns={[
                    { title: t('field.lot'), dataIndex: 'lot', render: (v: string) => v || '–' },
                    { title: t('massBalance.serials'), dataIndex: 'serials' },
                    { title: t('massBalance.commissioned'), dataIndex: 'commissioned' },
                    { title: t('massBalance.shipped'), dataIndex: 'shipped' },
                    { title: t('massBalance.received'), dataIndex: 'received' },
                    { title: t('massBalance.decommissioned'), dataIndex: 'decommissioned' },
                    { title: t('massBalance.destroyed'), dataIndex: 'destroyed' },
                    { title: t('massBalance.discrepancy'), render: (_v, r) => `${r.discrepancy} / ${r.allowed}` },
                    { title: t('field.status'), render: (_v, r) => (r.closed ? <Tag color="success">{t('massBalance.closes')}</Tag> : <Tag color="error">{t('massBalance.doesNotClose')}</Tag>) },
                  ]}
                />
              ),
            },
            {
              key: 'gaps',
              label: `${t('massBalance.gaps')} (${allGaps.length})`,
              children: (
                <Table<CustodyGap & { key: string }>
                  size="small"
                  loading={gaps.isLoading}
                  locale={{ emptyText: <Empty description={submitted ? t('massBalance.noGaps') : undefined} /> }}
                  dataSource={allGaps}
                  columns={[
                    { title: t('massBalance.severity'), dataIndex: 'severity', render: (v: string) => <StatusTag status={v} /> },
                    { title: t('massBalance.kind'), dataIndex: 'kind' },
                    { title: t('field.lot'), dataIndex: 'lot', render: (v: string) => v || '–' },
                    { title: t('field.serial'), dataIndex: 'epc', ellipsis: true },
                    { title: t('massBalance.from'), dataIndex: 'from', ellipsis: true },
                    { title: t('massBalance.to'), dataIndex: 'to', ellipsis: true },
                    { title: t('massBalance.seenAt'), dataIndex: 'at', render: (v: string) => formatDateTime(v) },
                    { title: t('massBalance.detail'), dataIndex: 'detail' },
                  ]}
                />
              ),
            },
            {
              key: 'reports',
              label: t('massBalance.reports'),
              children: (
                <Table<BalanceReportRow>
                  size="small"
                  rowKey="id"
                  loading={reports.isLoading}
                  dataSource={reports.data?.items ?? []}
                  columns={[
                    { title: t('field.gtin'), dataIndex: 'gtin' },
                    { title: t('field.lot'), dataIndex: 'lot', render: (v: string) => v || '–' },
                    { title: t('field.status'), dataIndex: 'status', render: (v: string) => <StatusTag status={v === 'balanced' ? 'ok' : 'error'} /> },
                    { title: t('massBalance.discrepancy'), render: (_v, r) => `${r.discrepancy} / ${r.allowed}` },
                    { title: t('field.createdAt'), dataIndex: 'createdAt', render: (v: string) => formatDateTime(v) },
                  ]}
                />
              ),
            },
            {
              key: 'settings',
              label: t('massBalance.settings'),
              children: form && (
                <Space direction="vertical">
                  <Space wrap>
                    <Typography.Text>{t('massBalance.toleranceUnits')}</Typography.Text>
                    <InputNumber min={0} max={1000000} value={form.toleranceUnits} disabled={!canWrite} onChange={(v) => setForm({ ...form, toleranceUnits: v ?? 0 })} />
                    <Typography.Text>{t('massBalance.tolerancePercent')}</Typography.Text>
                    <InputNumber min={0} max={100} step={0.1} value={form.tolerancePercent} disabled={!canWrite} onChange={(v) => setForm({ ...form, tolerancePercent: v ?? 0 })} />
                    {form.isDefault && <Tag>{t('massBalance.defaultSettings')}</Tag>}
                  </Space>
                  <Checkbox checked={form.proposeCase} disabled={!canWrite} onChange={(e) => setForm({ ...form, proposeCase: e.target.checked })}>{t('massBalance.proposeCase')}</Checkbox>
                  <Checkbox checked={form.balanceAuto} disabled={!canWrite} onChange={(e) => setForm({ ...form, balanceAuto: e.target.checked })}>{t('massBalance.balanceAuto')}</Checkbox>
                  <Space>
                    <Typography.Text>{t('massBalance.checkpointHours')}</Typography.Text>
                    <InputNumber min={0} max={8760} value={form.checkpointHours} disabled={!canWrite} onChange={(v) => setForm({ ...form, checkpointHours: v ?? 0 })} />
                  </Space>
                  {canWrite && <Button type="primary" loading={save.isPending} onClick={() => save.mutate(form)}>{t('action.save')}</Button>}
                </Space>
              ),
            },
          ]}
        />
      </Space>
    </PageContainer>
  );
}
