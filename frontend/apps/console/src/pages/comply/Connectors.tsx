import { useState } from 'react';
import { Alert, Button, Card, Col, Empty, Modal, Row, Space, Tabs, Tag, Tooltip, Typography } from 'antd';
import { ApiOutlined, CloudSyncOutlined, EditOutlined, FileTextOutlined, KeyOutlined, LinkOutlined, SendOutlined, WarningOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, JsonSchemaFields, ModalForm, PageContainer, prettyJson, ProFormDependency, ProFormRadio, ProFormSwitch, ProFormText, ProFormTextArea,
  QueryTable, StatusTag, type ConnectorConfigField, type ConnectorDef, type ConnectorInstance, type Report, type SftpFile,
} from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';

const connectorId = (c: ConnectorDef) => c.id ?? c.name;
const SFTP = 'sftp';
const specPending = (s?: string) => !!s && s.includes('TODO(spec)');
const isIntegration = (c?: ConnectorDef) => c?.extra?.category === 'integration';

type InstanceValues = { name?: string; enabled?: boolean; config?: Record<string, string> };

/** Reads the fingerprint an SFTP server presents, for comparison with the partner's; it never pins it. */
function HostKeyHelper({ host, port }: { host?: string; port?: string }) {
  const { t } = useTranslation();
  const [fp, setFp] = useState<string | null>(null);
  const read = useAction((v: { host: string; port: number }) => api.post<{ fingerprint: string }>('/connectors/sftp/host-key', v), {
    success: false,
    onSuccess: (r) => setFp(r.fingerprint),
  });
  return (
    <Space direction="vertical" style={{ width: '100%', marginBottom: 16 }}>
      <Button icon={<KeyOutlined />} disabled={!host} loading={read.isPending} onClick={() => read.mutate({ host: host ?? '', port: Number(port) || 22 })}>
        {t('connectorsUi.readHostKey')}
      </Button>
      {fp && (
        <Alert
          type="warning"
          showIcon
          message={<Typography.Text code copyable>{fp}</Typography.Text>}
          description={t('connectorsUi.hostKeyHint')}
        />
      )}
    </Space>
  );
}

function ConfigField({ f, stored }: { f: ConnectorConfigField; stored: boolean }) {
  const { t } = useTranslation();
  const name = ['config', f.key];
  const todo = specPending(f.help);
  const extra = f.help ? (todo ? <Typography.Text type="warning">{f.help}</Typography.Text> : f.help) : undefined;
  const required = f.required && !f.default && !stored;
  const rules = required ? [{ required: true, message: t('common.required') }] : undefined;
  const placeholder = f.secret && stored ? t('connectorsUi.secretKeep') : f.default;
  if (f.key === 'privateKey') {
    return <ProFormTextArea name={name} label={f.label} extra={extra} rules={rules} placeholder={placeholder} fieldProps={{ rows: 4, style: { fontFamily: 'monospace' }, autoComplete: 'off' }} />;
  }
  if (f.secret) {
    return <ProFormText.Password name={name} label={f.label} extra={extra} rules={rules} placeholder={placeholder} fieldProps={{ autoComplete: 'new-password' }} />;
  }
  return <ProFormText name={name} label={f.label} extra={extra} rules={rules} placeholder={placeholder} />;
}

function InstanceForm({ def, instance }: { def?: ConnectorDef; instance?: ConnectorInstance }) {
  const { t } = useTranslation();
  const fields = def?.config ?? [];
  const save = useSubmit(
    (v: InstanceValues) => {
      const config: Record<string, string> = {};
      for (const f of fields) {
        const val = v.config?.[f.key];
        if (f.secret && !val) continue; // empty secrets keep the stored value
        config[f.key] = val ?? '';
      }
      const body = { connector: instance?.connector ?? (def && connectorId(def)), name: v.name, config, enabled: v.enabled ?? true };
      return instance ? api.put(`/connectors/instances/${instance.id}`, body) : api.post('/connectors/instances', body);
    },
    { invalidate: [['/connectors/instances']] },
  );
  const initialConfig: Record<string, string> = {};
  for (const f of fields) {
    if (f.secret) continue;
    const cur = instance?.config?.[f.key];
    initialConfig[f.key] = typeof cur === 'string' ? cur : (f.default ?? '');
  }
  const schema = def?.configSchema ?? def?.schema;
  return (
    <ModalForm<InstanceValues>
      title={`${def?.title ?? def?.name ?? instance?.connector}`}
      width={640}
      trigger={instance ? <a><EditOutlined /> {t('action.edit')}</a> : <Button type="primary" icon={<ApiOutlined />}>{t('connectorsUi.configure')}</Button>}
      modalProps={{ destroyOnHidden: true }}
      initialValues={{ name: instance?.name ?? def?.name, enabled: instance?.enabled ?? true, config: initialConfig }}
      onFinish={save}
    >
      {specPending(def?.spec) && <Alert type="warning" showIcon style={{ marginBottom: 16 }} message={t('connectorsUi.specPending')} description={def?.spec} />}
      <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
      <ProFormSwitch name="enabled" label={t('field.enabled')} />
      {fields.map((f) => <ConfigField key={f.key} f={f} stored={!!instance?.secretsSet?.[f.key]} />)}
      {def && connectorId(def) === SFTP && (
        <ProFormDependency name={[['config', 'host'], ['config', 'port']]}>
          {({ config }) => <HostKeyHelper host={config?.host} port={config?.port} />}
        </ProFormDependency>
      )}
      {!fields.length && (schema?.properties ? <JsonSchemaFields schema={schema} name={['config']} /> : <Typography.Text type="secondary">{t('connectorsUi.noConfig')}</Typography.Text>)}
    </ModalForm>
  );
}

function SubmitForm({ inst }: { inst: ConnectorInstance }) {
  const { t } = useTranslation();
  const [report, setReport] = useState<Report | null>(null);
  const submit = useSubmit(
    (v: { mode: string; epcs?: string; captureId?: string }) =>
      api.post<Report>(`/connectors/instances/${inst.id}/submit`, v.mode === 'capture' ? { captureId: v.captureId } : { epcs: (v.epcs ?? '').split(/\s+/).filter(Boolean) }),
    { invalidate: [['/reports']], onSuccess: (r) => setReport(r) },
  );
  return (
    <>
      <ModalForm<{ mode: string; epcs?: string; captureId?: string }>
        title={`${t('action.submit')} · ${inst.name}`}
        trigger={<a><SendOutlined /> {t('action.submit')}</a>}
        modalProps={{ destroyOnHidden: true }}
        initialValues={{ mode: 'epcs' }}
        onFinish={submit}
      >
        <ProFormRadio.Group name="mode" options={[{ value: 'epcs', label: 'EPCs' }, { value: 'capture', label: 'Capture ID' }]} />
        <ProFormTextArea name="epcs" label="EPCs (one per line)" fieldProps={{ rows: 6, style: { fontFamily: 'monospace' } }} />
        <ProFormText name="captureId" label="Capture ID" />
      </ModalForm>
      <Modal open={!!report} onCancel={() => setReport(null)} footer={null} title={t('nav.reports')} width={640}>
        <CodeView code={prettyJson(report)} />
      </Modal>
    </>
  );
}

type TestResult = { ok: boolean; error?: string; presentedFingerprint?: string; folders?: Record<string, { path: string; files?: number; error?: string }> };
type PollResult = { processed: number; failed: number; files: SftpFile[] };

function FilesTable({ inst }: { inst: ConnectorInstance }) {
  const { t } = useTranslation();
  return (
    <QueryTable<SftpFile & Record<string, unknown>>
      rowKey="id"
      search={false}
      queryKey={[`/connectors/instances/${inst.id}/files`]}
      fetcher={() => api.list(`/connectors/instances/${inst.id}/files`)}
      expandable={{ rowExpandable: (r) => !!r.error, expandedRowRender: (r) => <Typography.Text type="danger">{r.error}</Typography.Text> }}
      columns={[
        { title: t('field.name'), dataIndex: 'name' },
        { title: t('connectorsUi.format'), dataIndex: 'format', width: 90 },
        { title: t('field.status'), dataIndex: 'status', width: 110, render: (_d, r) => <StatusTag status={r.status} /> },
        { title: t('connectorsUi.movedTo'), dataIndex: 'movedTo', ellipsis: true },
        { title: t('connectorsUi.size'), dataIndex: 'size', width: 90 },
        { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime', width: 170 },
      ]}
    />
  );
}

/** Connection test, poll-now and the inbound file log of an SFTP instance. */
function SftpActions({ inst }: { inst: ConnectorInstance }) {
  const { t } = useTranslation();
  const [test, setTest] = useState<TestResult | null>(null);
  const [poll, setPoll] = useState<PollResult | null>(null);
  const [files, setFiles] = useState(false);
  const testA = useAction(() => api.post<TestResult>(`/connectors/instances/${inst.id}/sftp/test`), { success: false, onSuccess: setTest });
  const pollA = useAction(() => api.post<PollResult>(`/connectors/instances/${inst.id}/sftp/poll`), {
    success: false,
    invalidate: [[`/connectors/instances/${inst.id}/files`]],
    onSuccess: setPoll,
  });
  return (
    <Space size="small" wrap>
      <a onClick={() => testA.mutate(undefined)}><LinkOutlined /> {t('connectorsUi.testConnection')}</a>
      <a onClick={() => pollA.mutate(undefined)}><CloudSyncOutlined /> {t('connectorsUi.pollNow')}</a>
      <a onClick={() => setFiles(true)}><FileTextOutlined /> {t('connectorsUi.files')}</a>
      <Modal open={!!test} onCancel={() => setTest(null)} footer={null} title={t('connectorsUi.testConnection')} width={640}>
        {test && (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Alert
              type={test.ok ? 'success' : 'error'}
              showIcon
              message={test.ok ? t('connectorsUi.connected') : t('connectorsUi.connectFailed')}
              description={test.presentedFingerprint ? t('connectorsUi.presentedKey', { fp: test.presentedFingerprint }) : test.error}
            />
            {test.folders && <CodeView code={prettyJson(test.folders)} maxHeight={240} />}
          </Space>
        )}
      </Modal>
      <Modal open={!!poll} onCancel={() => setPoll(null)} footer={null} title={t('connectorsUi.pollNow')} width={720}>
        {poll && (
          <>
            <Typography.Paragraph>{t('connectorsUi.polled', { processed: poll.processed, failed: poll.failed })}</Typography.Paragraph>
            <CodeView code={prettyJson(poll.files)} maxHeight={320} />
          </>
        )}
      </Modal>
      <Modal open={files} onCancel={() => setFiles(false)} footer={null} title={`${t('connectorsUi.files')} · ${inst.name}`} width={960} destroyOnHidden>
        <FilesTable inst={inst} />
      </Modal>
    </Space>
  );
}

export default function Connectors() {
  const { t } = useTranslation();
  const defs = useQuery({ queryKey: ['/connectors'], queryFn: () => api.list<ConnectorDef>('/connectors') });
  const byId = new Map((defs.data?.items ?? []).map((d) => [connectorId(d), d]));
  return (
    <PageContainer title={t('nav.connectors')} subTitle={t('connectorsUi.subtitle')}>
      <Tabs
        items={[
          {
            key: 'available',
            label: t('connectorsUi.available'),
            children: defs.data?.items.length ? (
              <Row gutter={[16, 16]}>
                {defs.data.items.map((c) => (
                  <Col xs={24} md={12} xl={8} key={connectorId(c)}>
                    <Card
                      title={c.title ?? c.name}
                      extra={
                        <Space size={4}>
                          {specPending(c.spec) && (
                            <Tooltip title={t('connectorsUi.specPending')}>
                              <Tag icon={<WarningOutlined />} color="warning">TODO(spec)</Tag>
                            </Tooltip>
                          )}
                          {isIntegration(c) ? <Tag color="blue">{t('connectorsUi.integration')}</Tag> : c.country && <Tag>{c.country}</Tag>}
                        </Space>
                      }
                      actions={[<InstanceForm key="cfg" def={c} />]}
                    >
                      <Typography.Paragraph type="secondary" ellipsis={{ rows: 3 }}>{c.description ?? connectorId(c)}</Typography.Paragraph>
                    </Card>
                  </Col>
                ))}
              </Row>
            ) : (
              <Card loading={defs.isLoading}><Empty /></Card>
            ),
          },
          {
            key: 'instances',
            label: t('connectorsUi.instances'),
            children: (
              <QueryTable<ConnectorInstance & Record<string, unknown>>
                rowKey="id"
                queryKey={['/connectors/instances']}
                fetcher={() => api.list('/connectors/instances')}
                expandable={{ expandedRowRender: (r) => <CodeView code={prettyJson(r.config ?? {})} maxHeight={240} /> }}
                columns={[
                  { title: t('field.name'), dataIndex: 'name' },
                  { title: 'Connector', dataIndex: 'connector' },
                  { title: t('field.enabled'), dataIndex: 'enabled', render: (_d, r) => <StatusTag status={r.enabled === false ? 'disabled' : 'active'} /> },
                  { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
                  {
                    title: t('common.actions'),
                    valueType: 'option',
                    render: (_d, r) => [
                      <InstanceForm key="e" def={byId.get(r.connector)} instance={r} />,
                      r.connector === SFTP ? <SftpActions key="sftp" inst={r} /> : <SubmitForm key="s" inst={r} />,
                    ],
                  },
                ]}
              />
            ),
          },
        ]}
      />
    </PageContainer>
  );
}
