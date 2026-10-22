import { useEffect, useState } from 'react';
import { Card, Col, Empty, Input, Row, Space, Tag, Timeline, Tree, Typography } from 'antd';
import type { DataNode } from 'antd/es/tree';
import { useQuery } from '@tanstack/react-query';
import { useSearchParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, ApiError, formatDateTime, PageContainer, ProDescriptions, StatusTag, type SerialTrace, type TreeNode } from '@openspoor/ui/pro';
import { cbv, locationId } from '../../lib/epcis';

function toTreeData(n: TreeNode, seen = new Set<string>()): DataNode {
  seen.add(n.epc);
  return {
    key: n.epc,
    title: <Typography.Text code>{n.epc}</Typography.Text>,
    children: (n.children ?? []).filter((c) => !seen.has(c.epc)).map((c) => toTreeData(c, seen)),
  };
}

export default function SerialLookup() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const epc = params.get('epc') ?? '';
  const [input, setInput] = useState(epc);
  useEffect(() => setInput(epc), [epc]);
  const enc = encodeURIComponent(epc);

  const trace = useQuery({ queryKey: ['/trace/serials', epc], queryFn: () => api.get<SerialTrace>(`/trace/serials/${enc}`), enabled: !!epc, meta: { silent: true } });
  const tree = useQuery({ queryKey: ['/trace/tree', epc], queryFn: () => api.get<TreeNode>(`/trace/tree/${enc}`), enabled: !!epc, meta: { silent: true } });
  const notFound = trace.error instanceof ApiError && trace.error.status === 404;
  const go = (v: string) => setParams(v.trim() ? { epc: v.trim() } : {});

  return (
    <PageContainer title={t('nav.serialLookup')}>
      <Card style={{ marginBottom: 16 }}>
        <Input.Search
          size="large"
          allowClear
          enterButton={t('action.lookup')}
          placeholder="urn:epc:id:sgtin:0614141.107346.2017 or https://id.gs1.org/01/09506000134352/21/ABC"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onSearch={go}
          loading={trace.isFetching}
        />
      </Card>
      {!epc ? (
        <Empty description={t('common.empty')} />
      ) : trace.error ? (
        <Card>
          <Empty description={notFound ? t('verify.notFound') : (trace.error as Error).message} />
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          <Col xs={24} lg={14}>
            <Card title={t('field.status')} loading={trace.isLoading} style={{ marginBottom: 16 }}>
              <ProDescriptions
                column={{ xs: 1, md: 2 }}
                dataSource={trace.data?.state ?? {}}
                columns={[
                  { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={(r as { status?: string }).status} /> },
                  { title: t('field.disposition'), dataIndex: 'disposition', render: (_d, r) => cbv((r as { disposition?: string }).disposition) },
                  { title: t('field.bizLocation'), dataIndex: 'bizLocation', copyable: true },
                  { title: 'Parent', dataIndex: 'parentEpc', render: (_d, r) => { const p = (r as { parentEpc?: string }).parentEpc; return p ? <a onClick={() => go(p)}>{p}</a> : '–'; } },
                  { title: 'Last event', dataIndex: 'lastEventTime', valueType: 'dateTime' },
                ]}
              />
              {!!trace.data?.ancestors?.length && (
                <Space wrap style={{ marginTop: 12 }}>
                  <Typography.Text type="secondary">Ancestors:</Typography.Text>
                  {trace.data.ancestors.map((a) => (
                    <Tag key={a} style={{ cursor: 'pointer' }} onClick={() => go(a)}>
                      {a}
                    </Tag>
                  ))}
                </Space>
              )}
            </Card>
            <Card title="History" loading={trace.isLoading}>
              {trace.data?.history?.length ? (
                <Timeline
                  mode="start"
                  items={trace.data.history.map((ev, i) => ({
                    key: ev.eventID ?? i,
                    color: /decommission|destroy|recall/i.test(String(ev.bizStep)) ? 'red' : /ship|receiv/i.test(String(ev.bizStep)) ? 'blue' : 'green',
                    title: formatDateTime(ev.eventTime),
                    content: (
                      <Space direction="vertical" size={0}>
                        <Typography.Text strong>
                          {cbv(ev.bizStep)} <Typography.Text type="secondary">· {ev.type} {ev.action ?? ''}</Typography.Text>
                        </Typography.Text>
                        <Typography.Text type="secondary">
                          {cbv(ev.disposition)} {locationId(ev.bizLocation) ? `@ ${locationId(ev.bizLocation)}` : ''}
                        </Typography.Text>
                      </Space>
                    ),
                  }))}
                />
              ) : (
                <Empty />
              )}
            </Card>
          </Col>
          <Col xs={24} lg={10}>
            <Card title="Aggregation" loading={tree.isLoading}>
              {tree.data ? (
                <Tree
                  showLine
                  defaultExpandAll
                  treeData={[toTreeData(tree.data)]}
                  onSelect={(keys) => keys[0] && keys[0] !== epc && go(String(keys[0]))}
                />
              ) : trace.data?.children?.length ? (
                <Space direction="vertical">
                  {trace.data.children.map((c) => (
                    <a key={c} onClick={() => go(c)}>
                      {c}
                    </a>
                  ))}
                </Space>
              ) : (
                <Empty />
              )}
            </Card>
          </Col>
        </Row>
      )}
    </PageContainer>
  );
}
