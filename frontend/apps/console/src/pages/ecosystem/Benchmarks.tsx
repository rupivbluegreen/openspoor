import { Alert, Button, Card, Col, Empty, Row, Space, Statistic, Tag, Tooltip, Typography } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, EyeInvisibleOutlined, PlusOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, ModalForm, PageContainer, prettyJson, ProFormDateTimePicker, ProFormText, ProFormTextArea, QueryTable,
  type BenchmarkKpi, type BenchmarkRun, type Benchmarks as BenchmarksResponse,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import { useSession } from '../../lib/session';

const fmt = (v: number | null | undefined, unit: string) => (v === null || v === undefined ? '–' : `${Math.round(v * 10) / 10}${unit === '%' ? '%' : ` ${unit}`}`);

function KpiCard({ k, minPeers }: { k: BenchmarkKpi; minPeers: number }) {
  const suppressed = k.peerMedian === null || k.peerMedian === undefined;
  let verdict: { better: boolean; label: string } | null = null;
  if (!suppressed && k.yours !== null && k.yours !== undefined) {
    const diff = k.yours - (k.peerMedian as number);
    const better = diff === 0 ? true : k.lowerIsBetter ? diff < 0 : diff > 0;
    verdict = { better, label: diff === 0 ? 'At peer median' : better ? 'Better than peer median' : 'Worse than peer median' };
  }
  return (
    <Card title={k.name} extra={<Tag>{k.lowerIsBetter ? 'lower is better' : 'higher is better'}</Tag>} style={{ height: '100%' }}>
      <Row gutter={16}>
        <Col span={8}>
          <Statistic title="Yours" value={fmt(k.yours, k.unit)} />
        </Col>
        <Col span={8}>
          <Statistic title="Peer median" value={suppressed ? '–' : fmt(k.peerMedian, k.unit)} />
        </Col>
        <Col span={8}>
          <Statistic title="Peer P90" value={suppressed ? '–' : fmt(k.peerP90, k.unit)} />
        </Col>
      </Row>
      <Space style={{ marginTop: 16 }} wrap>
        {suppressed ? (
          <Tooltip title={`Peer figures are hidden until at least ${minPeers} other tenants have data (k-anonymity).`}>
            <Tag icon={<EyeInvisibleOutlined />} color="default">
              Suppressed · {k.peers}/{minPeers} peers
            </Tag>
          </Tooltip>
        ) : (
          <Tag color="processing">{k.peers} peers</Tag>
        )}
        {verdict && (
          <Tag color={verdict.better ? 'success' : 'warning'} icon={verdict.better === !k.lowerIsBetter ? <ArrowUpOutlined /> : <ArrowDownOutlined />}>
            {verdict.label}
          </Tag>
        )}
        {(k.yours === null || k.yours === undefined) && <Typography.Text type="secondary">No data for your tenant in the last 30 days</Typography.Text>}
      </Space>
    </Card>
  );
}

type RunRow = BenchmarkRun & Record<string, unknown>;

interface RunForm {
  measuredAt: string;
  environment: string;
  version?: string;
  results: string;
}

export default function Benchmarks() {
  const { t } = useTranslation();
  const { principal } = useSession();
  const q = useQuery({ queryKey: ['/benchmarks'], queryFn: () => api.get<BenchmarksResponse>('/benchmarks') });
  const record = useSubmit(
    (v: RunForm) => api.post<BenchmarkRun>('/benchmarks/runs', { ...v, measuredAt: dayjs(v.measuredAt).toISOString(), results: JSON.parse(v.results) as unknown }),
    { invalidate: [['/benchmarks/system']] },
  );
  const kpis = q.data?.kpis ?? [];
  const minPeers = q.data?.minPeers ?? 5;
  const anySuppressed = kpis.some((k) => k.peerMedian === null || k.peerMedian === undefined);

  return (
    <PageContainer title={t('nav.benchmarks')} subTitle="Your operational KPIs against anonymized peers on this installation">
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        {anySuppressed && <Alert type="info" showIcon title="Some peer cohorts are suppressed" description={q.data?.note ?? `Peer figures appear only when at least ${minPeers} other tenants have data.`} />}
        {q.isLoading ? (
          <Card loading />
        ) : !kpis.length ? (
          <Card>
            <Empty description={t('common.empty')} />
          </Card>
        ) : (
          <Row gutter={[16, 16]}>
            {kpis.map((k) => (
              <Col key={k.key} xs={24} lg={12} xxl={8}>
                <KpiCard k={k} minPeers={minPeers} />
              </Col>
            ))}
          </Row>
        )}
        <QueryTable<RunRow>
          headerTitle="System performance runs"
          rowKey="id"
          queryKey={['/benchmarks/system']}
          fetcher={() => api.list<RunRow>('/benchmarks/system')}
          scroll={{ x: 'max-content' }}
          expandable={{ expandedRowRender: (r) => <CodeView code={prettyJson(r.results)} maxHeight={300} /> }}
          columns={[
            { title: 'Measured', dataIndex: 'measuredAt', valueType: 'dateTime' },
            { title: 'Environment', dataIndex: 'environment' },
            { title: t('field.version'), dataIndex: 'version', render: (_d, r) => r.version || '–' },
            {
              title: 'Results',
              dataIndex: 'results',
              render: (_d, r) =>
                Object.entries(r.results ?? {})
                  .slice(0, 4)
                  .map(([k, v]) => (
                    <Tag key={k}>
                      {k}: {typeof v === 'object' ? '…' : String(v)}
                    </Tag>
                  )),
            },
          ]}
          toolBarRender={() => [
            principal?.operator ? (
              <ModalForm<RunForm>
                key="run"
                title="Record benchmark run"
                trigger={
                  <Button type="primary" icon={<PlusOutlined />}>
                    Record run
                  </Button>
                }
                modalProps={{ destroyOnHidden: true }}
                initialValues={{ results: '{\n  "captureEventsPerSec": 0\n}' }}
                onFinish={record}
              >
                <ProFormDateTimePicker name="measuredAt" label="Measured at" rules={[{ required: true }]} />
                <ProFormText name="environment" label="Environment" placeholder="e.g. 4 vCPU / 16 GB, Postgres 17" rules={[{ required: true }]} />
                <ProFormText name="version" label={t('field.version')} />
                <ProFormTextArea
                  name="results"
                  label="Results (JSON object)"
                  fieldProps={{ rows: 8, style: { fontFamily: 'monospace' } }}
                  rules={[
                    { required: true },
                    {
                      validator: async (_r, v: string) => {
                        const o = JSON.parse(v) as unknown;
                        if (!o || typeof o !== 'object' || Array.isArray(o) || !Object.keys(o).length) throw new Error('Must be a non-empty JSON object');
                      },
                    },
                  ]}
                />
              </ModalForm>
            ) : null,
          ]}
        />
      </Space>
    </PageContainer>
  );
}
