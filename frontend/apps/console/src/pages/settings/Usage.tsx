import { Card, Col, Progress, Row, Statistic, Tag } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, PageContainer, ProDescriptions, type TenantUsage } from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';

export default function Usage() {
  const { t } = useTranslation();
  const { tenant } = useSession();
  const q = useQuery({ queryKey: ['/tenant/usage'], queryFn: () => api.get<TenantUsage>('/tenant/usage') });
  const features = useQuery({ queryKey: ['/tenant/features'], queryFn: () => api.get<Record<string, unknown>>('/tenant/features'), meta: { silent: true } });
  const u = q.data;
  const quota = u?.quota ?? {};
  const metric = (key: 'events' | 'reports' | 'activePartners', label: string) => {
    const used = u?.[key] ?? 0;
    const max = quota[key];
    return (
      <Col xs={24} md={8} key={key}>
        <Card loading={q.isLoading}>
          <Statistic title={label} value={used} suffix={max ? `/ ${max.toLocaleString()}` : undefined} />
          {max ? <Progress percent={Math.min(100, Math.round((used / max) * 100))} status={used > max ? 'exception' : 'normal'} /> : null}
        </Card>
      </Col>
    );
  };
  const featureEntries = Object.entries((features.data as { items?: unknown } | undefined)?.items ?? features.data ?? {});
  return (
    <PageContainer title={t('nav.usage')} subTitle={u?.period}>
      <Card style={{ marginBottom: 16 }}>
        <ProDescriptions
          column={{ xs: 1, md: 3 }}
          dataSource={tenant ?? {}}
          columns={[
            { title: t('field.tenant'), dataIndex: 'name' },
            { title: t('field.plan'), dataIndex: 'plan', render: (_d, r) => <Tag color="green">{String((r as { plan?: string }).plan ?? '–')}</Tag> },
            { title: 'Region', dataIndex: 'region' },
          ]}
        />
      </Card>
      <Row gutter={[16, 16]}>
        {metric('events', 'EPCIS events')}
        {metric('reports', 'Regulatory reports')}
        {metric('activePartners', 'Active partners')}
      </Row>
      {featureEntries.length > 0 && (
        <Card title="Features" style={{ marginTop: 16 }}>
          {featureEntries.map(([k, v]) => (
            <Tag key={k} color={v === false ? 'default' : 'green'}>
              {Array.isArray(features.data) ? String(v) : k}
            </Tag>
          ))}
        </Card>
      )}
    </PageContainer>
  );
}
