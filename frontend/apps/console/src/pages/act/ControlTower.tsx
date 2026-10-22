import { Badge, Button, Card, Col, Empty, List, Row, Statistic, Table, Tag, Typography } from 'antd';
import { PlayCircleOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, formatDateTime, PageContainer, statusColor, StatusTag, type AgentDef, type Case, type ControlTower as CT } from '@openspoor/ui/pro';
import { useAction } from '../../lib/hooks';

const PRIORITY: Record<string, number> = { critical: 0, p0: 0, high: 1, p1: 1, medium: 2, p2: 2, low: 3, p3: 3 };
const rank = (p: Case['priority']) => (typeof p === 'number' ? p : (PRIORITY[String(p ?? '').toLowerCase()] ?? 9));

export default function ControlTower() {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ['/control-tower'], queryFn: () => api.get<CT>('/control-tower'), refetchInterval: 30_000 });
  const agents = useQuery({ queryKey: ['/agents'], queryFn: () => api.list<AgentDef>('/agents'), meta: { silent: true } });
  const runAgent = useAction((name: string) => api.post(`/agents/${encodeURIComponent(name)}/run`), { invalidate: [['/control-tower'], ['/agents/actions']] });
  const cases = [...(q.data?.cases ?? q.data?.queue ?? [])].sort((a, b) => rank(a.priority) - rank(b.priority));

  return (
    <PageContainer title={t('nav.controlTower')}>
      <Row gutter={[16, 16]}>
        {(q.data?.monitors ?? []).map((m, i) => (
          <Col xs={12} md={8} xl={6} key={m.id ?? m.name ?? i}>
            <Card size="small">
              <Statistic
                title={
                  <span>
                    <Badge status={statusColor(m.status) as 'success' | 'error' | 'warning' | 'processing' | 'default'} /> {m.name ?? m.id}
                  </span>
                }
                value={m.value ?? m.status ?? '–'}
              />
              {m.description && <Typography.Text type="secondary">{m.description}</Typography.Text>}
            </Card>
          </Col>
        ))}
        {!q.isLoading && !q.data?.monitors?.length && (
          <Col span={24}>
            <Card><Empty description="No monitors" /></Card>
          </Col>
        )}
        <Col xs={24} xl={16}>
          <Card title="Work queue" extra={<Link to="/act/cases">{t('nav.cases')}</Link>}>
            <Table<Case>
              size="small"
              rowKey={(r) => r.id}
              loading={q.isLoading}
              dataSource={cases}
              pagination={{ pageSize: 10 }}
              columns={[
                { title: t('field.priority'), dataIndex: 'priority', render: (v) => <Tag color={statusColor(String(v))}>{String(v ?? '–')}</Tag>, width: 100 },
                { title: t('field.title'), dataIndex: 'title' },
                { title: t('field.kind'), dataIndex: 'kind' },
                { title: t('field.status'), dataIndex: 'status', render: (v) => <StatusTag status={v as string} /> },
                { title: t('field.createdAt'), dataIndex: 'createdAt', render: (v) => formatDateTime(v as string) },
              ]}
            />
          </Card>
        </Col>
        <Col xs={24} xl={8}>
          <Card title="Agents" extra={<Link to="/act/approvals">{t('nav.agentApprovals')}</Link>}>
            <List
              loading={agents.isLoading}
              dataSource={agents.data?.items ?? []}
              renderItem={(a) => (
                <List.Item
                  actions={[
                    <Button key="run" size="small" icon={<PlayCircleOutlined />} loading={runAgent.isPending && runAgent.variables === a.name} onClick={() => runAgent.mutate(a.name)}>
                      {t('action.run')}
                    </Button>,
                  ]}
                >
                  <List.Item.Meta title={a.name} description={a.description} />
                </List.Item>
              )}
            />
          </Card>
        </Col>
      </Row>
    </PageContainer>
  );
}
