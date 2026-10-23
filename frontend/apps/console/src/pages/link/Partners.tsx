import { useState } from 'react';
import { Alert, Button, Card, Space, Tabs, Tag, Typography } from 'antd';
import { CheckOutlined, MailOutlined, PlusOutlined, SettingOutlined } from '@ant-design/icons';
import { useSearchParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, CodeView, glnValidator, ModalForm, PageContainer, ProFormText, QueryTable, StatusTag, type Partner } from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';
import { NetworkDirectory } from './NetworkDirectory';
import { PartnerDrawer } from './PartnerDrawer';

type Row = Partner & Record<string, unknown>;

function health(h: Partner['health']) {
  if (!h) return '–';
  if (typeof h === 'string') return <StatusTag status={h} />;
  const s = (h as { status?: string }).status;
  return s ? <StatusTag status={s} /> : <Typography.Text code>{JSON.stringify(h)}</Typography.Text>;
}

function Connections() {
  const { t } = useTranslation();
  const approve = useAction((id: string) => api.post(`/partners/${id}/approve`), { invalidate: [['/partners']] });
  const create = useSubmit((v: Record<string, unknown>) => api.post('/partners', v), { invalidate: [['/partners']] });
  const focusId = useSearchParams()[0].get('id');
  const [open, setOpen] = useState<Partner | null>(null);
  return (
    <>
    <QueryTable<Row>
      rowKey="id"
      queryKey={['/partners']}
      tableId="partners"
      focus={focusId ? { id: focusId } : undefined}
      fetcher={() => api.list<Row>('/partners')}
      columns={[
        { title: t('field.name'), dataIndex: 'name' },
        { title: 'GLN', dataIndex: 'gln', copyable: true },
        { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
        { title: t('field.health'), dataIndex: 'health', render: (_d, r) => health(r.health) },
        { title: 'Endpoints', dataIndex: 'endpoints', render: (_d, r) => Object.keys(r.endpoints ?? {}).map((k) => <Tag key={k}>{k}</Tag>) },
        {
          title: t('common.actions'),
          valueType: 'option',
          render: (_d, r) => [
            <a key="manage" onClick={() => setOpen(r)}>
              <SettingOutlined /> Onboarding
            </a>,
            ...(['pending', 'invited', 'requested'].includes(String(r.status).toLowerCase())
              ? [
                  <a key="approve" onClick={() => approve.mutate(r.id)}>
                    <CheckOutlined /> {t('action.approve')}
                  </a>,
                ]
              : []),
          ],
        },
      ]}
      toolBarRender={() => [
        <ModalForm<Record<string, unknown>>
          key="create"
          title={t('nav.partners')}
          trigger={<Button type="primary" icon={<PlusOutlined />}>{t('action.create')}</Button>}
          modalProps={{ destroyOnHidden: true }}
          onFinish={create}
        >
          <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
          <ProFormText name="gln" label="GLN" rules={[glnValidator(t('common.invalidGln'))]} />
          <ProFormText name={['endpoints', 'epcisCapture']} label="EPCIS endpoint" rules={[{ type: 'url' }]} />
          <ProFormText name={['endpoints', 'as2Url']} label="AS2 endpoint" rules={[{ type: 'url' }]} />
          <ProFormText name={['endpoints', 'vrs']} label="VRS endpoint" rules={[{ type: 'url' }]} />
          <ProFormText name={['endpoints', 'federation']} label="Federation peer id" />
        </ModalForm>,
      ]}
    />
    <PartnerDrawer partner={open} onClose={() => setOpen(null)} />
    </>
  );
}

function Invitations() {
  const { t } = useTranslation();
  const [code, setCode] = useState<string | null>(null);
  const invite = useSubmit((v: { email: string; name: string }) => api.post<{ code?: string; invitationCode?: string }>('/partners/invitations', v), {
    onSuccess: (r) => setCode(r?.code ?? r?.invitationCode ?? JSON.stringify(r)),
  });
  const accept = useSubmit((v: { code: string }) => api.post('/partners/invitations/accept', v), { invalidate: [['/partners']] });
  return (
    <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 640 }}>
      <Card title={<span><MailOutlined /> {t('action.invite')}</span>}>
        <ModalForm<{ email: string; name: string }>
          title={t('action.invite')}
          trigger={<Button type="primary">{t('action.invite')}</Button>}
          modalProps={{ destroyOnHidden: true }}
          onFinish={invite}
        >
          <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
          <ProFormText name="email" label={t('field.email')} rules={[{ required: true }, { type: 'email' }]} />
        </ModalForm>
        {code && (
          <div style={{ marginTop: 16 }}>
            <Alert type="success" showIcon title="Invitation code" style={{ marginBottom: 8 }} />
            <CodeView code={code} />
          </div>
        )}
      </Card>
      <Card title={t('action.accept')}>
        <ModalForm<{ code: string }> title={t('action.accept')} trigger={<Button>{t('action.accept')}</Button>} modalProps={{ destroyOnHidden: true }} onFinish={accept}>
          <ProFormText name="code" label={t('field.code')} rules={[{ required: true }]} />
        </ModalForm>
      </Card>
    </Space>
  );
}

export default function Partners() {
  const { t } = useTranslation();
  return (
    <PageContainer title={t('nav.partners')}>
      <Tabs
        items={[
          { key: 'conn', label: 'Connections', children: <Connections /> },
          { key: 'dir', label: 'Directory', children: <NetworkDirectory /> },
          { key: 'inv', label: 'Invitations', children: <Invitations /> },
        ]}
      />
    </PageContainer>
  );
}
