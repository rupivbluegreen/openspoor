import { useState } from 'react';
import { Alert, Button, Table, Typography } from 'antd';
import { SafetyOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { api, notifyError, PageContainer, QueryTable, type AuditEntry, type AuditVerify } from '@openspoor/ui/pro';
import { diffJson, type DiffRow } from '../../lib/diff';

type Row = AuditEntry & Record<string, unknown>;

export default function Audit() {
  const { t } = useTranslation();
  const [verify, setVerify] = useState<AuditVerify | null>(null);
  const [busy, setBusy] = useState(false);

  const runVerify = async () => {
    setBusy(true);
    try {
      setVerify(await api.get<AuditVerify>('/audit/verify'));
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <PageContainer
      title={t('nav.audit')}
      subTitle="Hash-chained, tamper-evident"
      extra={
        <Button type="primary" icon={<SafetyOutlined />} loading={busy} onClick={runVerify}>
          {t('action.verifyChain')}
        </Button>
      }
    >
      {verify && (
        <Alert
          closable={{ onClose: () => setVerify(null) }}
          style={{ marginBottom: 16 }}
          showIcon
          type={verify.intact ? 'success' : 'error'}
          title={verify.intact ? `Chain intact · ${verify.checked} entries checked` : `Chain BROKEN at seq ${verify.brokenAtSeq} (${verify.checked} checked)`}
        />
      )}
      <QueryTable<Row>
        rowKey="seq"
        queryKey={['/audit']}
        serverPagination
        pageSize={50}
        search={{ labelWidth: 'auto' }}
        fetcher={({ current, pageSize, ...q }) => api.list<Row>('/audit', { ...(q as Record<string, string>), limit: pageSize, offset: (current - 1) * pageSize })}
        expandable={{
          rowExpandable: (r) => r.before !== undefined || r.after !== undefined,
          expandedRowRender: (r) => (
            <Table<DiffRow>
              size="small"
              rowKey="path"
              pagination={false}
              dataSource={diffJson(r.before ?? {}, r.after ?? {})}
              columns={[
                { title: 'Field', dataIndex: 'path' },
                { title: 'Before', dataIndex: 'before', render: (v) => <Typography.Text code>{JSON.stringify(v)}</Typography.Text> },
                { title: 'After', dataIndex: 'after', render: (v) => <Typography.Text code>{JSON.stringify(v)}</Typography.Text> },
              ]}
            />
          ),
        }}
        columns={[
          { title: t('field.seq'), dataIndex: 'seq', search: false, width: 80 },
          { title: t('field.at'), dataIndex: 'at', valueType: 'dateTime', search: false },
          { title: t('field.actor'), dataIndex: 'actor', search: false },
          { title: t('field.action'), dataIndex: 'action', tooltip: 'Prefix match' },
          { title: t('field.entity'), dataIndex: 'entity' },
          { title: t('field.entityId'), dataIndex: 'entityId', ellipsis: true, copyable: true },
          { title: t('field.reason'), dataIndex: 'reason', search: false, ellipsis: true },
          { title: t('field.hash'), dataIndex: 'hash', search: false, render: (_d, r) => <Typography.Text code copyable={{ text: r.hash }}>{r.hash?.slice(0, 12)}…</Typography.Text> },
        ]}
      />
    </PageContainer>
  );
}
