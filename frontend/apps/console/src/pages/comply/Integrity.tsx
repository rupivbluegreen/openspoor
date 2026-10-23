import { Alert, Button, Card, Descriptions, Space, Table, Tag, Typography } from 'antd';
import { SafetyCertificateOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, downloadBlob, formatDateTime, notifyError, notifySuccess, PageContainer, type IntegrityCheckpoint, type IntegrityNode, type IntegrityReport } from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';
import { useAction } from '../../lib/hooks';

const KEY = ['/trace/integrity/checkpoints'] as const;

/** Signed checkpoints over the audit chain head and EPCIS event batches; export and verify. */
export default function Integrity() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canWrite = hasRole('Compliance');
  const list = useQuery({ queryKey: KEY, queryFn: () => api.list<IntegrityCheckpoint>('/trace/integrity/checkpoints') });
  const node = useQuery({ queryKey: ['/trace/integrity/node'], queryFn: () => api.get<IntegrityNode>('/trace/integrity/node') });
  const [report, setReport] = useState<IntegrityReport | null>(null);
  const create = useAction(() => api.post('/trace/integrity/checkpoints'), { invalidate: [KEY], success: t('integrity.created') });
  const verify = useAction(() => api.get<IntegrityReport>('/trace/integrity/verify'), {
    success: false,
    onSuccess: (r) => {
      setReport(r);
      if (r.ok) notifySuccess(t('integrity.verified'));
      else notifyError(new Error(t('integrity.failed')));
    },
  });
  const download = async (c: IntegrityCheckpoint, evidence: boolean) => {
    const body = await api.get<string>(`/trace/integrity/checkpoints/${c.seq}`, evidence ? { evidence: 1 } : undefined, { responseType: 'text' });
    downloadBlob(body, `checkpoint-${c.seq}${evidence ? '-evidence' : ''}.json`, 'application/json');
  };
  const key = node.data?.publicKey ?? '<public key>';
  return (
    <PageContainer
      title={t('nav.integrity')}
      subTitle={t('integrity.subtitle')}
      extra={
        <Space>
          <Button icon={<SafetyCertificateOutlined />} loading={verify.isPending} onClick={() => verify.mutate(undefined)}>{t('integrity.verify')}</Button>
          {canWrite && <Button type="primary" loading={create.isPending} onClick={() => create.mutate(undefined)}>{t('integrity.create')}</Button>}
        </Space>
      }
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Alert type="info" showIcon message={t('integrity.note')} />
        <Card size="small" title={t('integrity.node')}>
          <Descriptions size="small" column={1}>
            <Descriptions.Item label={t('integrity.keyId')}>{node.data?.keyId}</Descriptions.Item>
            <Descriptions.Item label={t('integrity.publicKey')}><Typography.Text code copyable>{node.data?.publicKey}</Typography.Text></Descriptions.Item>
            <Descriptions.Item label={t('integrity.command')}><Typography.Text code copyable>{`openspoor verify-checkpoint --pubkey ${key} checkpoint-1-evidence.json`}</Typography.Text></Descriptions.Item>
          </Descriptions>
        </Card>
        {report && (
          <Alert
            type={report.ok ? 'success' : 'error'}
            showIcon
            message={report.ok ? t('integrity.verified') : t('integrity.failed')}
            description={
              <>
                <div>{t('integrity.auditChain')}: {report.auditChain.ok ? 'ok' : `broken at ${report.auditChain.brokenAt}`} ({t('integrity.checked')}: {report.auditChain.checked})</div>
                {report.checkpoints.map((c) => (
                  <div key={c.seq}>#{c.seq}: {c.checks.map((x) => <Tag key={x.name} color={x.ok ? 'success' : 'error'}>{x.name}</Tag>)}</div>
                ))}
              </>
            }
          />
        )}
        <Table<IntegrityCheckpoint>
          size="small"
          rowKey="seq"
          loading={list.isLoading}
          dataSource={list.data?.items ?? []}
          columns={[
            { title: t('integrity.seq'), dataIndex: 'seq' },
            { title: t('integrity.signedAt'), dataIndex: 'createdAt', render: (v: string) => formatDateTime(v) },
            { title: t('integrity.auditSeq'), dataIndex: 'auditSeq' },
            { title: t('integrity.events'), dataIndex: 'eventCount' },
            { title: t('integrity.root'), dataIndex: 'eventRoot', ellipsis: true, render: (v: string) => <Typography.Text code>{v.slice(0, 16)}…</Typography.Text> },
            {
              title: t('common.actions'),
              render: (_v, c) => (
                <Space>
                  <a onClick={() => void download(c, false)}>{t('integrity.download')}</a>
                  <a onClick={() => void download(c, true)}>{t('integrity.withEvidence')}</a>
                </Space>
              ),
            },
          ]}
        />
      </Space>
    </PageContainer>
  );
}
