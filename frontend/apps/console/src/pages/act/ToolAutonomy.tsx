import { Card, Collapse, Select, Space, Table, Tag, Tooltip, Typography } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, StatusTag, type AgentTool } from '@openspoor/ui/pro';
import { useAction } from '../../lib/hooks';

const KEY = ['/agents/tools'] as const;

/** Autonomy ladder per action type: suggest only, apply after approval, or auto-apply (low-risk reversible tools only). */
export function ToolAutonomy() {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: KEY, queryFn: () => api.list<AgentTool>('/agents/tools') });
  const set = useAction((v: { name: string; mode: string }) => api.put(`/agents/tools/${v.name}`, { mode: v.mode }), { invalidate: [KEY, ['/agents/actions']] });
  return (
    <Collapse
      items={[
        {
          key: 'autonomy',
          label: t('agents.autonomy'),
          children: (
            <Card variant="borderless" styles={{ body: { padding: 0 } }}>
              <Typography.Paragraph type="secondary">{t('agents.autonomyHint')}</Typography.Paragraph>
              <Table<AgentTool>
                size="small"
                rowKey="name"
                loading={q.isLoading}
                pagination={false}
                dataSource={q.data?.items ?? []}
                columns={[
                  { title: t('agents.actionType'), dataIndex: 'name', render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
                  { title: t('agents.risk'), dataIndex: 'risk', render: (v: string) => <StatusTag status={v} /> },
                  { title: t('agents.reversible'), dataIndex: 'reversible', render: (v: boolean) => (v ? <Tag color="success">✓</Tag> : <Tag>–</Tag>) },
                  { title: t('agents.usedBy'), dataIndex: 'agents', render: (v?: string[]) => <Space wrap>{(v ?? []).map((n) => <Tag key={n}>{n}</Tag>)}</Space> },
                  {
                    title: t('agents.mode'),
                    dataIndex: 'mode',
                    render: (_v, r) => (
                      <Space direction="vertical" size={4}>
                      <Select
                        size="small"
                        style={{ minWidth: 240 }}
                        value={r.mode}
                        loading={set.isPending}
                        onChange={(mode) => set.mutate({ name: r.name, mode })}
                        options={[
                          { value: 'suggest', label: t('agents.modeSuggest') },
                          { value: 'approve', label: t('agents.modeApprove') },
                          { value: 'auto', label: <Tooltip title={r.autoAllowed ? undefined : t('agents.autoNotAllowed')}>{t('agents.modeAuto')}</Tooltip>, disabled: !r.autoAllowed },
                        ]}
                      />
                      {!!r.evalBlocked?.length && <Tooltip title={r.evalBlocked.join('; ')}><Tag color="warning">{t('evals.blocked')}</Tag></Tooltip>}
                      </Space>
                    ),
                  },
                ]}
              />
            </Card>
          ),
        },
      ]}
    />
  );
}
