import { useState } from 'react';
import { Alert, Button, Card, Col, Empty, Result, Row, Segmented, Space, Table, Tag, Typography } from 'antd';
import { DownloadOutlined } from '@ant-design/icons';
import { Bar, Pie } from '@ant-design/charts';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, ApiError, downloadCsv, formatDateTime, PageContainer, useAppSettings, type HubAnalytics } from '@openspoor/ui/pro';

type Org = HubAnalytics['organisations'][number];

/** Organisations that have not reported for this many days (or at all in the period) are highlighted. */
export const SILENT_AFTER_DAYS = 7;

export function isSilent(o: Pick<Org, 'daysSilent'>): boolean {
  return o.daysSilent === null || o.daysSilent >= SILENT_AFTER_DAYS;
}

/** Rows for the CSV export: one section per table, flattened with a "table" column. */
export function analyticsCsv(d: HubAnalytics): { header: string[]; rows: (string | number)[][] } {
  const header = ['table', 'key', 'name', 'events', 'captures', 'rejected', 'lastReportAt', 'daysSilent', 'owner', 'serials', 'failedScans', 'failedVerifications', 'count'];
  const blank = (n: number) => Array<string>(n).fill('');
  const rows: (string | number)[][] = [
    ...d.organisations.map((o) => ['organisation', o.tenantId, o.name, o.events, o.captures, o.rejectedCaptures, o.lastReportAt ?? '', o.daysSilent ?? '', ...blank(5)]),
    ...d.serialStates.map((s) => ['serialState', s.key, ...blank(10), s.count]),
    ...d.openAlerts.map((s) => ['openAlert', s.key, ...blank(10), s.count]),
    ...d.products.map((p) => ['product', p.gtin, p.name, ...blank(5), p.owner, p.serials, p.failedScans, p.failedVerifications, '']),
  ];
  return { header, rows };
}

export default function Analytics() {
  const { t } = useTranslation();
  const { mode } = useAppSettings();
  const [days, setDays] = useState(30);
  const q = useQuery({
    queryKey: ['/operator/hub/analytics', days],
    queryFn: () => api.get<HubAnalytics>('/operator/hub/analytics', { days }),
    retry: false,
    meta: { silent: true },
  });
  const chartTheme = mode === 'dark' ? 'classicDark' : 'classic';
  const notHub = q.error instanceof ApiError && q.error.status === 404;
  const d = q.data;
  const silent = (d?.organisations ?? []).filter(isSilent).length;

  const header = (
    <Space wrap>
      <Typography.Text>{t('operator.analytics.period')}</Typography.Text>
      <Segmented
        value={days}
        onChange={(v) => setDays(Number(v))}
        options={[7, 30, 90].map((n) => ({ value: n, label: t('operator.analytics.lastDays', { n }) }))}
      />
      <Button
        icon={<DownloadOutlined />}
        disabled={!d}
        onClick={() => {
          if (!d) return;
          const { header: h, rows } = analyticsCsv(d);
          downloadCsv(`hub-analytics-${d.days}d`, h, rows);
        }}
      >
        {t('operator.analytics.exportCsv')}
      </Button>
    </Space>
  );

  if (notHub) {
    return (
      <PageContainer title={t('operator.analytics.title')}>
        <Result status="info" title={t('operator.analytics.notHubTitle')} subTitle={t('operator.analytics.notHubDesc')} />
      </PageContainer>
    );
  }
  if (q.isError) {
    return (
      <PageContainer title={t('operator.analytics.title')} content={header}>
        <Alert type="error" showIcon title={(q.error as Error).message} />
      </PageContainer>
    );
  }

  const orgChart = (d?.organisations ?? []).filter((o) => o.events > 0).slice(0, 15).map((o) => ({ name: o.name, events: o.events }));
  return (
    <PageContainer title={t('operator.analytics.title')} content={header}>
      <Row gutter={[16, 16]}>
        <Col xs={24}>
          <Card
            title={t('operator.analytics.organisations')}
            loading={q.isLoading}
            extra={silent > 0 ? <Tag color="warning">{t('operator.analytics.silentCount', { n: silent })}</Tag> : null}
          >
            <Typography.Paragraph type="secondary">{t('operator.analytics.silentNote')}</Typography.Paragraph>
            <Table<Org>
              size="small"
              rowKey="tenantId"
              scroll={{ x: 'max-content' }}
              dataSource={d?.organisations ?? []}
              locale={{ emptyText: <Empty description={t('operator.analytics.noData')} /> }}
              onRow={(o) => (isSilent(o) ? { style: { background: 'var(--ant-color-warning-bg, #fffbe6)' } } : {})}
              pagination={{ pageSize: 15, hideOnSinglePage: true }}
              columns={[
                { title: t('operator.analytics.organisation'), dataIndex: 'name', sorter: (a, b) => a.name.localeCompare(b.name) },
                { title: t('operator.analytics.events'), dataIndex: 'events', align: 'right', sorter: (a, b) => a.events - b.events, render: (v: number) => v.toLocaleString() },
                { title: t('operator.analytics.captures'), dataIndex: 'captures', align: 'right', sorter: (a, b) => a.captures - b.captures },
                { title: t('operator.analytics.rejected'), dataIndex: 'rejectedCaptures', align: 'right', sorter: (a, b) => a.rejectedCaptures - b.rejectedCaptures, render: (v: number) => (v > 0 ? <Typography.Text type="danger">{v}</Typography.Text> : v) },
                { title: t('operator.analytics.lastReport'), dataIndex: 'lastReportAt', render: (v: string | null) => (v ? formatDateTime(v) : <Typography.Text type="secondary">{t('operator.analytics.neverReported')}</Typography.Text>) },
                {
                  title: t('operator.analytics.daysSilent'),
                  dataIndex: 'daysSilent',
                  align: 'right',
                  sorter: (a, b) => (a.daysSilent ?? 1e9) - (b.daysSilent ?? 1e9),
                  render: (_v, o) => (isSilent(o) ? <Tag color="warning">{o.daysSilent ?? '–'} · {t('operator.analytics.silent')}</Tag> : o.daysSilent),
                },
              ]}
            />
          </Card>
        </Col>
        {orgChart.length > 0 && (
          <Col xs={24}>
            <Card title={t('operator.analytics.events')}>
              <Bar data={orgChart} xField="name" yField="events" height={Math.max(160, orgChart.length * 28)} theme={chartTheme} />
            </Card>
          </Col>
        )}
        <Col xs={24} lg={12}>
          <Card title={t('operator.analytics.serialStates')} loading={q.isLoading}>
            {d?.serialStates.length ? (
              <Pie data={d.serialStates} angleField="count" colorField="key" height={280} innerRadius={0.6} theme={chartTheme} legend={{ color: { position: 'bottom' } }} />
            ) : (
              <Empty description={t('operator.analytics.noData')} />
            )}
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card title={t('operator.analytics.openAlerts')} loading={q.isLoading}>
            {d?.openAlerts.length ? (
              <Bar data={d.openAlerts} xField="key" yField="count" height={280} theme={chartTheme} />
            ) : (
              <Empty description={t('operator.analytics.noData')} />
            )}
          </Card>
        </Col>
        <Col xs={24}>
          <Card title={t('operator.analytics.products')} loading={q.isLoading}>
            <Table<HubAnalytics['products'][number]>
              size="small"
              rowKey={(p) => `${p.owner}/${p.gtin}`}
              scroll={{ x: 'max-content' }}
              dataSource={d?.products ?? []}
              locale={{ emptyText: <Empty description={t('operator.analytics.noData')} /> }}
              pagination={{ pageSize: 15, hideOnSinglePage: true }}
              columns={[
                { title: t('operator.analytics.gtin'), dataIndex: 'gtin' },
                { title: t('operator.analytics.product'), dataIndex: 'name' },
                { title: t('operator.analytics.owner'), dataIndex: 'owner' },
                { title: t('operator.analytics.serials'), dataIndex: 'serials', align: 'right', render: (v: number) => v.toLocaleString() },
                { title: t('operator.analytics.failedScans'), dataIndex: 'failedScans', align: 'right', sorter: (a, b) => a.failedScans - b.failedScans },
                { title: t('operator.analytics.failedVerifications'), dataIndex: 'failedVerifications', align: 'right', sorter: (a, b) => a.failedVerifications - b.failedVerifications },
              ]}
            />
          </Card>
        </Col>
      </Row>
    </PageContainer>
  );
}
