import { useState } from 'react';
import { Button, Card, Col, Empty, Flex, Popconfirm, Progress, Row, Segmented, Space, Table, Tag, Typography } from 'antd';
import { CheckOutlined, CloseOutlined, RobotOutlined, RollbackOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { Bubble, Think, ThoughtChain, type ThoughtChainItemType } from '@ant-design/x';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, CodeView, formatDateTime, PageContainer, prettyJson, StatusTag, useESignature, type AgentAction } from '@openspoor/ui/pro';
import { useAction } from '../../lib/hooks';
import { diffJson, type DiffRow } from '../../lib/diff';
import { ProposalPreview } from './ProposalPreview';
import { ToolAutonomy } from './ToolAutonomy';

const show = (v: unknown) => (v === undefined ? <Typography.Text type="secondary">∅</Typography.Text> : <Typography.Text code>{typeof v === 'string' ? v : JSON.stringify(v)}</Typography.Text>);

function DiffView({ action }: { action: AgentAction }) {
  if (typeof action.diff === 'string') return <CodeView code={action.diff} maxHeight={260} />;
  const before = action.diff?.before ?? action.before;
  const after = action.diff?.after ?? action.after;
  if (before === undefined && after === undefined) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No diff" />;
  const rows = diffJson(before, after);
  return (
    <Table<DiffRow>
      size="small"
      rowKey="path"
      pagination={false}
      dataSource={rows}
      columns={[
        { title: 'Field', dataIndex: 'path' },
        { title: 'Before', dataIndex: 'before', render: (v) => <span style={{ textDecoration: 'line-through', opacity: 0.75 }}>{show(v)}</span> },
        { title: 'After', dataIndex: 'after', render: (v) => <strong>{show(v)}</strong> },
      ]}
      footer={() => <Typography.Text type="secondary">{rows.length} change(s)</Typography.Text>}
    />
  );
}

function reasoningSteps(r: AgentAction['reasoning']): ThoughtChainItemType[] {
  const steps = Array.isArray(r) ? r : typeof r === 'string' ? r.split(/\n+/).filter(Boolean) : [];
  return steps.map((s, i) => ({ key: String(i), title: `Step ${i + 1}`, description: s, status: 'success' }));
}

function ActionCard({ a, status }: { a: AgentAction; status: string }) {
  const { t } = useTranslation();
  const inv = [['/agents/actions'], ['/control-tower']] as const;
  const approve = useAction(() => api.post(`/agents/actions/${a.id}/approve`), { invalidate: inv });
  const reject = useAction(() => api.post(`/agents/actions/${a.id}/reject`), { invalidate: inv });
  const revert = useAction(() => api.post(`/agents/actions/${a.id}/revert`), { invalidate: inv });
  const { requestSignature, holder } = useESignature();
  const steps = reasoningSteps(a.reasoning);
  const s = (a.status ?? status).toLowerCase();

  return (
    <Card
      title={
        <Space>
          <RobotOutlined />
          {a.title ?? a.kind ?? a.id}
          <StatusTag status={a.status ?? status} />
        </Space>
      }
      extra={<Typography.Text type="secondary">{a.agent} · {formatDateTime(a.createdAt)}</Typography.Text>}
      actions={
        s === 'pending'
          ? [
              <Button key="ok" type="link" icon={<CheckOutlined />} loading={approve.isPending} disabled={a.autonomy === 'suggest'} onClick={() => approve.mutate(undefined)}>
                {t('action.approve')}
              </Button>,
              <Button
                key="sign"
                type="link"
                icon={<SafetyCertificateOutlined />}
                disabled={a.autonomy === 'suggest'}
                onClick={async () => {
                  if (await requestSignature({ entity: 'agent_action', entityId: a.id, defaultMeaning: 'approval' })) approve.mutate(undefined);
                }}
              >
                {t('action.sign')} & {t('action.approve')}
              </Button>,
              <Popconfirm key="no" title={`${t('action.reject')}?`} onConfirm={() => reject.mutateAsync(undefined)}>
                <Button type="link" danger icon={<CloseOutlined />} loading={reject.isPending}>
                  {t('action.reject')}
                </Button>
              </Popconfirm>,
            ]
          : (s === 'approved' || s === 'applied' || s === 'executed') && a.reversible !== false
            ? [
                <Popconfirm key="rev" title={`${t('action.revert')}?`} description={t('common.audited')} onConfirm={() => revert.mutateAsync(undefined)}>
                  <Button type="link" icon={<RollbackOutlined />} loading={revert.isPending}>
                    {t('action.revert')}
                  </Button>
                </Popconfirm>,
              ]
            : []
      }
    >
      <Row gutter={[16, 16]}>
        <Col xs={24} lg={12}>
          {a.summary && <Bubble placement="start" variant="outlined" avatar={<RobotOutlined />} content={a.summary} style={{ marginBottom: 12 }} />}
          {steps.length > 1 ? (
            <ThoughtChain items={steps} />
          ) : a.reasoning ? (
            <Think title="Reasoning" defaultExpanded>
              {String(Array.isArray(a.reasoning) ? a.reasoning.join('\n') : a.reasoning)}
            </Think>
          ) : null}
          {typeof a.confidence === 'number' && (
            <Flex align="center" gap={8} style={{ marginTop: 12 }}>
              <Typography.Text type="secondary">Confidence</Typography.Text>
              <Progress percent={Math.round(a.confidence <= 1 ? a.confidence * 100 : a.confidence)} size="small" style={{ flex: 1 }} />
            </Flex>
          )}
        </Col>
        <Col xs={24} lg={12}>
          <ProposalPreview action={a} fallback={<DiffView action={a} />} />
        </Col>
      </Row>
      {holder}
    </Card>
  );
}

export default function AgentApprovals() {
  const { t } = useTranslation();
  const [status, setStatus] = useState('pending');
  const q = useQuery({ queryKey: ['/agents/actions', status], queryFn: () => api.list<AgentAction>('/agents/actions', { status }) });
  return (
    <PageContainer
      title={t('nav.agentApprovals')}
      subTitle={t('common.audited')}
      extra={
        <Segmented
          value={status}
          onChange={(v) => setStatus(String(v))}
          options={['pending', 'executed', 'failed', 'rejected', 'reverted'].map((s) => ({ value: s, label: t(`agents.${s}`) }))}
        />
      }
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <ToolAutonomy />
        {q.isLoading && <Card loading />}
        {!q.isLoading && !q.data?.items.length && (
          <Card>
            <Empty description={t('common.empty')} />
          </Card>
        )}
        {q.data?.items.map((a) => <ActionCard key={a.id} a={a} status={status} />)}
        {!!q.data?.items.length && (
          <details>
            <summary>
              <Tag>raw</Tag>
            </summary>
            <CodeView code={prettyJson(q.data.items)} maxHeight={300} />
          </details>
        )}
      </Space>
    </PageContainer>
  );
}
