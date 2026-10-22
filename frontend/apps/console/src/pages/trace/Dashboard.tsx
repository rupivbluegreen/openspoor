import { Card, Col, Empty, List, Row, Statistic, Typography } from 'antd';
import { AlertOutlined, BarcodeOutlined, CloudUploadOutlined, NodeIndexOutlined } from '@ant-design/icons';
import { Column, Pie } from '@ant-design/charts';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, formatDateTime, PageContainer, StatusTag, useAppSettings, type Alert, type CaptureJob, type TraceStats } from '@openspoor/ui/pro';

export default function Dashboard() {
  const { t } = useTranslation();
  const { mode } = useAppSettings();
  const stats = useQuery({ queryKey: ['/trace/stats'], queryFn: () => api.get<TraceStats>('/trace/stats'), refetchInterval: 30_000 });
  const captures = useQuery({ queryKey: ['/epcis/capture', 'recent'], queryFn: () => api.list<CaptureJob>('/epcis/capture'), meta: { silent: true } });
  const alerts = useQuery({ queryKey: ['/alerts', 'open'], queryFn: () => api.list<Alert>('/alerts', { status: 'open' }), meta: { silent: true } });

  const byStatus = Object.entries(stats.data?.byStatus ?? {}).map(([status, count]) => ({ status, count }));
  const chartTheme = mode === 'dark' ? 'classicDark' : 'classic';
  const s = stats.data;

  return (
    <PageContainer title={t('nav.dashboard')}>
      <Row gutter={[16, 16]}>
        {[
          { title: t('field.events'), value: s?.events, icon: <NodeIndexOutlined /> },
          { title: 'Serials', value: s?.serials, icon: <BarcodeOutlined /> },
          { title: 'Captures (24h)', value: s?.captures24h, icon: <CloudUploadOutlined /> },
          { title: t('nav.alerts'), value: alerts.data?.total, icon: <AlertOutlined /> },
        ].map((x) => (
          <Col xs={12} lg={6} key={x.title}>
            <Card loading={stats.isLoading}>
              <Statistic title={x.title} value={x.value ?? 0} prefix={x.icon} />
            </Card>
          </Col>
        ))}
        <Col xs={24} lg={12}>
          <Card title="Serials by status" loading={stats.isLoading}>
            {byStatus.length ? (
              <Pie data={byStatus} angleField="count" colorField="status" height={280} innerRadius={0.6} theme={chartTheme} legend={{ color: { position: 'bottom' } }} />
            ) : (
              <Empty />
            )}
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card title="Status distribution" loading={stats.isLoading}>
            {byStatus.length ? <Column data={byStatus} xField="status" yField="count" height={280} theme={chartTheme} /> : <Empty />}
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card title={t('nav.epcisInbox')} extra={<Link to="/trace/epcis">{t('action.view')}</Link>}>
            <List
              loading={captures.isLoading}
              dataSource={(captures.data?.items ?? []).slice(0, 6)}
              locale={{ emptyText: <Empty description={t('common.empty')} /> }}
              renderItem={(c) => (
                <List.Item extra={<StatusTag status={c.status} />}>
                  <List.Item.Meta
                    title={<Link to={`/trace/epcis/${c.id ?? c.captureId}`}>{c.id ?? c.captureId}</Link>}
                    description={`${c.eventCount ?? 0} events · ${c.errorCount ?? c.errors?.length ?? 0} errors · ${formatDateTime(c.createdAt)}`}
                  />
                </List.Item>
              )}
            />
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card title={t('nav.alerts')} extra={<Link to="/comply/alerts">{t('action.view')}</Link>}>
            <List
              loading={alerts.isLoading}
              dataSource={(alerts.data?.items ?? []).slice(0, 6)}
              locale={{ emptyText: <Empty description={t('common.empty')} /> }}
              renderItem={(a) => (
                <List.Item extra={<StatusTag status={a.severity} />}>
                  <List.Item.Meta title={a.title} description={<Typography.Text type="secondary">{a.kind} · {formatDateTime(a.createdAt)}</Typography.Text>} />
                </List.Item>
              )}
            />
          </Card>
        </Col>
      </Row>
    </PageContainer>
  );
}
