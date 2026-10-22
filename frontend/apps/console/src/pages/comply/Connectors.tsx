import { useState } from 'react';
import { Button, Card, Col, Empty, Modal, Row, Tabs, Tag, Typography } from 'antd';
import { ApiOutlined, SendOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, JsonSchemaFields, ModalForm, PageContainer, prettyJson, ProFormRadio, ProFormSwitch, ProFormText, ProFormTextArea,
  QueryTable, StatusTag, type ConnectorDef, type ConnectorInstance, type Report,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';

const connectorId = (c: ConnectorDef) => c.id ?? c.name;

function InstanceForm({ def, instance, onDone }: { def?: ConnectorDef; instance?: ConnectorInstance; onDone?: () => void }) {
  const { t } = useTranslation();
  const save = useSubmit(
    (v: Record<string, unknown>) => api.post('/connectors/instances', { connector: instance?.connector ?? (def && connectorId(def)), name: v.name, config: v.config ?? {}, enabled: v.enabled ?? true }),
    { invalidate: [['/connectors/instances']], onSuccess: onDone },
  );
  const schema = def?.configSchema ?? def?.schema;
  return (
    <ModalForm<Record<string, unknown>>
      title={`${def?.title ?? def?.name ?? instance?.connector}`}
      width={640}
      trigger={<Button type="primary" icon={<ApiOutlined />}>Configure</Button>}
      modalProps={{ destroyOnHidden: true }}
      initialValues={{ name: def?.name, enabled: true, ...(instance ?? {}) }}
      onFinish={save}
    >
      <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
      <ProFormSwitch name="enabled" label={t('field.enabled')} />
      {schema?.properties ? <JsonSchemaFields schema={schema} name={['config']} /> : <Typography.Text type="secondary">No configuration schema published by this connector.</Typography.Text>}
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

export default function Connectors() {
  const { t } = useTranslation();
  const defs = useQuery({ queryKey: ['/connectors'], queryFn: () => api.list<ConnectorDef>('/connectors') });
  return (
    <PageContainer title={t('nav.connectors')} subTitle="EU FMD (EMVS), Saudi RSD, …">
      <Tabs
        items={[
          {
            key: 'available',
            label: 'Available',
            children: defs.data?.items.length ? (
              <Row gutter={[16, 16]}>
                {defs.data.items.map((c) => (
                  <Col xs={24} md={12} xl={8} key={connectorId(c)}>
                    <Card title={c.title ?? c.name} extra={c.country && <Tag>{c.country}</Tag>} actions={[<InstanceForm key="cfg" def={c} />]}>
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
            label: 'Instances',
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
                  { title: t('common.actions'), valueType: 'option', render: (_d, r) => [<SubmitForm key="s" inst={r} />] },
                ]}
              />
            ),
          },
        ]}
      />
    </PageContainer>
  );
}
