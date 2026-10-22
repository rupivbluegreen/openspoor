import { useState } from 'react';
import { Alert, Button, Descriptions, Modal, Popconfirm, Tag, Typography } from 'antd';
import { CodeOutlined, ExperimentOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, ModalForm, PageContainer, ProFormSelect, ProFormText, ProFormTextArea, QueryTable, ROLES,
  type AppListing, type AppManifest, type SandboxResult,
} from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';
import { useSession } from '../../lib/session';

type Row = AppListing & Record<string, unknown>;

const KINDS = ['app', 'connector', 'agent', 'rulepack', 'integration'];

export default function DeveloperApps() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const [sandbox, setSandbox] = useState<SandboxResult | null>(null);
  const publish = useSubmit((v: AppManifest) => api.post<AppListing>('/apps', v), { invalidate: [['/apps'], ['/marketplace']] });
  const createSandbox = useAction(() => api.post<SandboxResult>('/apps/sandbox'), { onSuccess: (r) => setSandbox(r) });

  return (
    <PageContainer
      title={t('nav.developerApps')}
      subTitle="Publish packages to the marketplace and provision isolated sandboxes"
      content={
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Community packages are listed unverified until an operator verifies them. Installing an app grants it an API key with exactly the roles its manifest requests
          (never Admin); a <Typography.Text code>webhook</Typography.Text> (https only) receives EPCIS subscriptions.
        </Typography.Paragraph>
      }
    >
      <QueryTable<Row>
        rowKey="id"
        queryKey={['/apps']}
        fetcher={() => api.list<Row>('/apps')}
        scroll={{ x: 'max-content' }}
        columns={[
          { title: t('field.id'), dataIndex: 'id', render: (_d, r) => <Typography.Text code>{r.id}</Typography.Text> },
          { title: t('field.name'), dataIndex: 'name' },
          { title: t('field.kind'), dataIndex: 'kind', render: (_d, r) => <Tag color="blue">{r.kind}</Tag> },
          { title: 'Publisher', dataIndex: 'publisher' },
          { title: t('field.version'), dataIndex: 'version' },
          { title: t('field.roles'), dataIndex: 'roles', render: (_d, r) => (r.roles?.length ? r.roles.map((x) => <Tag key={x}>{x}</Tag>) : '–') },
          { title: 'Webhook', dataIndex: 'webhook', ellipsis: true, render: (_d, r) => r.webhook || '–' },
          { title: t('field.status'), render: (_d, r) => (r.verified ? <Tag color="success">verified</Tag> : <Tag color="warning">community</Tag>) },
          { title: 'Installed', dataIndex: 'installed', render: (_d, r) => (r.installed ? t('common.yes') : t('common.no')) },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
        ]}
        toolBarRender={() => [
          hasRole('Integration') ? (
            <Popconfirm
              key="sandbox"
              title="Create a developer sandbox?"
              description="Provisions a new isolated trial tenant with sample data and an Integration API key."
              onConfirm={() => createSandbox.mutateAsync(undefined)}
            >
              <Button icon={<ExperimentOutlined />} loading={createSandbox.isPending}>
                Create sandbox
              </Button>
            </Popconfirm>
          ) : null,
          hasRole('Admin') ? (
            <ModalForm<AppManifest>
              key="publish"
              title="Publish app"
              width={600}
              trigger={
                <Button type="primary" icon={<CodeOutlined />}>
                  Publish app
                </Button>
              }
              modalProps={{ destroyOnHidden: true }}
              initialValues={{ kind: 'app', version: '1.0.0', roles: ['Viewer'] }}
              onFinish={publish}
            >
              <ProFormText
                name="id"
                label={t('field.id')}
                tooltip="Unique package id, no spaces or slashes"
                rules={[{ required: true }, { pattern: /^[^\s/]+$/, message: 'No spaces or slashes' }]}
              />
              <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
              <ProFormSelect name="kind" label={t('field.kind')} rules={[{ required: true }]} options={KINDS.map((k) => ({ value: k, label: k }))} />
              <ProFormText name="publisher" label="Publisher" rules={[{ required: true }]} />
              <ProFormText name="version" label={t('field.version')} rules={[{ required: true }]} />
              <ProFormTextArea name="description" label={t('field.description')} />
              <ProFormSelect
                name="roles"
                label={t('field.roles')}
                mode="multiple"
                tooltip="Roles the app's API key receives on install"
                options={ROLES.filter((r) => r !== 'Admin').map((r) => ({ value: r, label: r }))}
              />
              <ProFormText name="webhook" label="Webhook" placeholder="https://…" rules={[{ pattern: /^https:\/\//, message: 'Must be https://' }]} />
              <ProFormText name="homepage" label="Homepage" placeholder="https://…" />
            </ModalForm>
          ) : null,
        ]}
      />
      <Modal
        open={!!sandbox}
        title="Sandbox created"
        width={640}
        onCancel={() => setSandbox(null)}
        onOk={() => setSandbox(null)}
        cancelButtonProps={{ style: { display: 'none' } }}
        maskClosable={false}
      >
        <Alert type="warning" showIcon title={t('common.secretOnce')} description={sandbox?.note} style={{ marginBottom: 12 }} />
        {sandbox && (
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label={t('field.tenant')}>
              <Typography.Text code copyable>
                {sandbox.tenant}
              </Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="Admin password">
              <Typography.Text code copyable>
                {sandbox.adminPassword}
              </Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="Sample GTIN">
              <Typography.Text code copyable>
                {sandbox.sampleGtin}
              </Typography.Text>
            </Descriptions.Item>
          </Descriptions>
        )}
        <Typography.Paragraph strong style={{ margin: '12px 0 4px' }}>
          API key (Integration, Serialization, Ops)
        </Typography.Paragraph>
        <CodeView code={sandbox?.apiKey ?? ''} />
      </Modal>
    </PageContainer>
  );
}
