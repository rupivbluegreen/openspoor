import { useState } from 'react';
import { Alert, Button, Modal, Popconfirm, Tag, Typography } from 'antd';
import { KeyOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import { api, CodeView, ModalForm, PageContainer, ProFormDatePicker, ProFormSelect, ProFormText, QueryTable, ROLES, type ApiKey } from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';
import { useSession } from '../../lib/session';

type Row = ApiKey & Record<string, unknown>;

export default function ApiKeys() {
  const { t } = useTranslation();
  const { principal } = useSession();
  const [secret, setSecret] = useState<string | null>(null);
  const create = useSubmit(
    (v: Record<string, unknown>) =>
      api.post<ApiKey>('/api-keys', { ...v, expiresAt: v.expiresAt ? dayjs(v.expiresAt as string).endOf('day').toISOString() : undefined }),
    { invalidate: [['/api-keys']], onSuccess: (k) => setSecret(k.secret ?? null) },
  );
  const revoke = useAction((id: string) => api.del(`/api-keys/${id}`), { invalidate: [['/api-keys']] });
  const grantable = ROLES.filter((r) => principal?.roles?.includes(r) || principal?.roles?.includes('Admin'));

  return (
    <PageContainer title={t('nav.apiKeys')} subTitle="X-API-Key: osk_…">
      <QueryTable<Row>
        rowKey="id"
        queryKey={['/api-keys']}
        fetcher={() => api.list<Row>('/api-keys')}
        columns={[
          { title: t('field.name'), dataIndex: 'name' },
          { title: 'Prefix', dataIndex: 'prefix', render: (_d, r) => <Typography.Text code>{r.prefix}…</Typography.Text> },
          { title: t('field.roles'), dataIndex: 'roles', render: (_d, r) => r.roles?.map((x) => <Tag key={x}>{x}</Tag>) },
          { title: 'Created by', dataIndex: 'createdBy' },
          { title: 'Last used', dataIndex: 'lastUsedAt', valueType: 'dateTime' },
          { title: 'Expires', dataIndex: 'expiresAt', valueType: 'date' },
          { title: t('field.status'), render: (_d, r) => (r.revokedAt ? <Tag>revoked</Tag> : <Tag color="success">active</Tag>) },
          {
            title: t('common.actions'),
            valueType: 'option',
            render: (_d, r) =>
              r.revokedAt
                ? []
                : [
                    <Popconfirm key="revoke" title={`${t('action.revoke')} ${r.name}?`} description={t('common.audited')} onConfirm={() => revoke.mutateAsync(r.id)}>
                      <a>{t('action.revoke')}</a>
                    </Popconfirm>,
                  ],
          },
        ]}
        toolBarRender={() => [
          <ModalForm<Record<string, unknown>>
            key="create"
            title={t('nav.apiKeys')}
            trigger={
              <Button type="primary" icon={<KeyOutlined />}>
                {t('action.create')}
              </Button>
            }
            modalProps={{ destroyOnHidden: true }}
            onFinish={create}
          >
            <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
            <ProFormSelect name="roles" label={t('field.roles')} mode="multiple" rules={[{ required: true }]} options={grantable.map((r) => ({ value: r, label: r }))} />
            <ProFormDatePicker name="expiresAt" label="Expires" />
          </ModalForm>,
        ]}
      />
      <Modal open={!!secret} title={t('nav.apiKeys')} onCancel={() => setSecret(null)} onOk={() => setSecret(null)} cancelButtonProps={{ style: { display: 'none' } }} maskClosable={false}>
        <Alert type="warning" showIcon title={t('common.secretOnce')} style={{ marginBottom: 12 }} />
        <CodeView code={secret ?? ''} />
      </Modal>
    </PageContainer>
  );
}
