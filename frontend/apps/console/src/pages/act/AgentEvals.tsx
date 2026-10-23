import { useEffect, useState } from 'react';
import { Alert, Button, InputNumber, Progress, Space, Table, Tag, Typography } from 'antd';
import { PlayCircleOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, formatDateTime, PageContainer, type AgentEvalItem, type AgentEvals as AgentEvalsResponse } from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';
import { useAction } from '../../lib/hooks';

const KEY = ['/agents/evals'] as const;

/** Measured scores of the governed agents on their synthetic golden datasets, and the bar the autonomy ladder applies. */
export default function AgentEvals() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canRun = hasRole('Compliance');
  const q = useQuery({ queryKey: KEY, queryFn: () => api.get<AgentEvalsResponse>('/agents/evals') });
  const [min, setMin] = useState<number | null>(null);
  useEffect(() => {
    if (q.data) setMin(q.data.minScore);
  }, [q.data]);
  const run = useAction(() => api.post('/agents/evals/run'), { invalidate: [KEY, ['/agents/tools']], success: t('evals.ran') });
  const save = useAction((v: number) => api.put('/agents/evals/policy', { minScore: v }), { invalidate: [KEY, ['/agents/tools']], success: t('common.saved') });
  return (
    <PageContainer
      title={t('nav.agentEvals')}
      subTitle={t('evals.subtitle')}
      extra={canRun && <Button type="primary" icon={<PlayCircleOutlined />} loading={run.isPending} onClick={() => run.mutate(undefined)}>{t('evals.run')}</Button>}
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Alert type="warning" showIcon message={t('evals.syntheticNote')} />
        {canRun && (
          <Space wrap>
            <Typography.Text>{t('evals.minScore')}</Typography.Text>
            <InputNumber min={0.5} max={1} step={0.01} value={min} onChange={setMin} />
            <Button disabled={min === null || min === q.data?.minScore} loading={save.isPending} onClick={() => min !== null && save.mutate(min)}>{t('evals.save')}</Button>
            {q.data?.minScoreIsDefault && <Tag>{t('evals.default')}</Tag>}
            <Typography.Text type="secondary">{t('evals.minScoreHint')}</Typography.Text>
          </Space>
        )}
        <Table<AgentEvalItem>
          size="small"
          rowKey="agent"
          loading={q.isLoading}
          pagination={false}
          dataSource={q.data?.items ?? []}
          columns={[
            { title: t('evals.agent'), dataIndex: 'agent', render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
            { title: t('evals.cases'), dataIndex: 'cases' },
            { title: t('evals.passed'), render: (_v, r) => (r.latest ? `${r.latest.passedCases}/${r.latest.cases}` : '–') },
            { title: t('evals.precision'), render: (_v, r) => (r.latest ? r.latest.precision.toFixed(2) : '–') },
            { title: t('evals.recall'), render: (_v, r) => (r.latest ? r.latest.recall.toFixed(2) : '–') },
            {
              title: t('evals.score'),
              render: (_v, r) => (r.latest ? <Progress percent={Math.round(r.latest.score * 100)} size="small" status={r.latest.meets ? 'success' : 'exception'} style={{ minWidth: 120 }} /> : '–'),
            },
            { title: t('evals.required'), render: (_v, r) => (r.latest ? r.latest.required.toFixed(2) : Math.max(q.data?.minScore ?? 0, r.passThreshold).toFixed(2)) },
            {
              title: t('evals.status'),
              render: (_v, r) => (!r.latest ? <Tag>{t('evals.noScore')}</Tag> : r.latest.meets ? <Tag color="success">{t('evals.meets')}</Tag> : <Tag color="error">{t('evals.below')}</Tag>),
            },
            { title: t('evals.lastRun'), render: (_v, r) => (r.latest ? formatDateTime(r.latest.runAt) : '–') },
          ]}
        />
      </Space>
    </PageContainer>
  );
}
