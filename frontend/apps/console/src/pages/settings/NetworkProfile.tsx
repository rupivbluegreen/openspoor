import { useState } from 'react';
import { Alert, Button, Card, Input, Modal, Select, Space, Table, Tabs, Tag, Typography } from 'antd';
import { DownloadOutlined, SafetyCertificateOutlined, StopOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import {
  api, downloadBlob, PageContainer, ProForm, ProFormList, ProFormSelect, ProFormSwitch, ProFormText, ProFormTextArea, StatusTag, type License,
} from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';
import { useOrgOptions } from '../../lib/options';
import type { IssuedCredential, Profile, ProfileLicense } from '../link/linkTypes';

const KINDS = ['manufacturer', 'wholesaler', '3pl', 'pharmacy', 'hospital', 'retailer', 'distributor', 'grower', 'processor'];
const tags = { mode: 'tags' as const, tokenSeparators: [',', ' '] };

function ProfileForm() {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ['/partners/listing'], queryFn: () => api.get<Profile>('/partners/listing') });
  const licenses = useQuery({ queryKey: ['/licenses'], queryFn: () => api.list<License>('/licenses') });
  const save = useSubmit((v: Partial<Profile>) => api.put('/partners/listing', { ...v, endpoints: q.data?.endpoints ?? {} }), { invalidate: [['/partners/listing']] });
  if (!q.data) return null;
  const p = q.data;
  const fromRegister = (l: License): ProfileLicense => ({
    kind: l.kind, number: l.number, authority: l.authority, country: l.country, validTo: l.validTo ? dayjs(l.validTo).format('YYYY-MM-DD') : undefined,
  });
  return (
    <Card>
      {p.name && (
        <Alert
          style={{ marginBottom: 16 }}
          showIcon
          type={p.verified ? 'success' : 'info'}
          title={p.verified ? `Verified by the network operator${p.verificationNote ? `: ${p.verificationNote}` : ''}` : 'Not verified yet. Changing the name, GLNs or company prefixes clears a verification.'}
        />
      )}
      <ProForm<Partial<Profile>>
        initialValues={{ ...p, kind: p.kind || 'manufacturer', licenses: p.licenses ?? [] }}
        onFinish={save}
        submitter={{ searchConfig: { submitText: t('action.save') }, resetButtonProps: false }}
      >
        <ProFormSwitch name="published" label="Publish in the network directory" />
        <ProForm.Group>
          <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} width="md" />
          <ProFormSelect name="kind" label={t('field.kind')} options={KINDS} rules={[{ required: true }]} width="sm" />
          <ProFormText name="country" label={t('field.country')} width="xs" fieldProps={{ maxLength: 2 }} />
        </ProForm.Group>
        <ProForm.Group>
          <ProFormText name="gln" label="Primary GLN" width="sm" />
          <ProFormSelect name="glns" label="GLNs" fieldProps={tags} width="md" />
          <ProFormSelect name="companyPrefixes" label="GS1 company prefixes" fieldProps={tags} width="md" />
        </ProForm.Group>
        <ProFormTextArea name="description" label="Description" fieldProps={{ rows: 3 }} />
        <ProForm.Group>
          <ProFormText name="website" label="Website" width="md" rules={[{ type: 'url' }]} />
          <ProFormText name="contact" label="Contact for connection requests (e-mail or https URL)" width="md" />
        </ProForm.Group>
        <ProFormList
          name="licenses"
          label="Licenses shown on the profile"
          creatorButtonProps={{ creatorButtonText: 'Add license' }}
          actionGuard={{}}
          itemRender={({ listDom, action }) => <Space align="start">{listDom}{action}</Space>}
        >
          {(_f, _i, act) => {
            const row = act.getCurrentRowData() as ProfileLicense | undefined;
            return (
              <ProForm.Group>
                <ProFormText name="kind" placeholder={t('field.kind')} rules={[{ required: true }]} width="xs" />
                <ProFormText name="number" placeholder={t('field.number')} rules={[{ required: true }]} width="xs" />
                <ProFormText name="authority" placeholder={t('field.authority')} rules={[{ required: true }]} width="sm" />
                <ProFormText name="validTo" placeholder="YYYY-MM-DD" width="xs" />
                {row?.verified ? <Tag color="success">verified</Tag> : <Tag>unverified</Tag>}
              </ProForm.Group>
            );
          }}
        </ProFormList>
        <Typography.Text type="secondary">From the license register: </Typography.Text>
        <Space wrap style={{ marginBottom: 16 }}>
          {(licenses.data?.items ?? []).map((l) => (
            <ProForm.Item key={l.id} noStyle shouldUpdate>
              {(form) => (
                <Button
                  size="small"
                  onClick={() => form.setFieldValue('licenses', [...((form.getFieldValue('licenses') as ProfileLicense[]) ?? []), fromRegister(l)])}
                >
                  + {l.kind} {l.number}
                </Button>
              )}
            </ProForm.Item>
          ))}
        </Space>
      </ProForm>
    </Card>
  );
}

function Credentials() {
  const { t } = useTranslation();
  const orgs = useOrgOptions();
  const licenses = useQuery({ queryKey: ['/licenses'], queryFn: () => api.list<License>('/licenses') });
  const creds = useQuery({ queryKey: ['/credentials'], queryFn: () => api.list<IssuedCredential>('/credentials') });
  const [licenseId, setLicenseId] = useState<string>();
  const [revoking, setRevoking] = useState<IssuedCredential | null>(null);
  const [reason, setReason] = useState('');
  const issue = useAction((id: string) => api.post('/credentials', { licenseId: id }), { invalidate: [['/credentials']] });
  const revoke = useAction((v: { id: string; reason: string }) => api.post(`/credentials/${v.id}/revoke`, { reason: v.reason }), {
    invalidate: [['/credentials']],
    onSuccess: () => setRevoking(null),
  });
  const label = (l: License) => `${l.kind} ${l.number} · ${orgs.find((o) => o.value === l.orgId)?.label ?? ''}`;
  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card size="small" title="Issue an authorized trading partner credential">
        <Space wrap>
          <Select style={{ minWidth: 320 }} placeholder="License" value={licenseId} onChange={setLicenseId}
            options={(licenses.data?.items ?? []).map((l) => ({ value: l.id, label: label(l) }))} />
          <Button type="primary" icon={<SafetyCertificateOutlined />} disabled={!licenseId} loading={issue.isPending} onClick={() => licenseId && issue.mutate(licenseId)}>
            Issue
          </Button>
        </Space>
        <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>
          A W3C Verifiable Credential signed by this node. Partners verify it against this node&apos;s DID and its public revocation list.
        </Typography.Paragraph>
      </Card>
      <Table<IssuedCredential>
        size="small"
        rowKey="id"
        loading={creds.isFetching}
        dataSource={creds.data?.items ?? []}
        columns={[
          { title: 'Credential', dataIndex: 'id', render: (id: string) => <Typography.Text code copyable>{id}</Typography.Text> },
          {
            title: 'License',
            dataIndex: 'licenseId',
            render: (id: string) => {
              const l = licenses.data?.items.find((x) => x.id === id);
              return l ? label(l) : id;
            },
          },
          { title: 'Valid until', dataIndex: 'validUntil', render: (v?: string) => (v ? dayjs(v).format('YYYY-MM-DD') : '–') },
          { title: t('field.status'), render: (_v, r) => <StatusTag status={r.revokedAt ? 'revoked' : 'active'} /> },
          {
            title: t('common.actions'),
            render: (_v, r) => (
              <Space>
                <Button size="small" icon={<DownloadOutlined />} onClick={() => downloadBlob(JSON.stringify(r.credential, null, 2), `credential-${r.id}.json`, 'application/json')}>
                  JSON
                </Button>
                {!r.revokedAt && (
                  <Button size="small" danger icon={<StopOutlined />} onClick={() => setRevoking(r)}>
                    {t('action.revoke')}
                  </Button>
                )}
              </Space>
            ),
          },
        ]}
      />
      <Modal
        open={!!revoking}
        title={t('action.revoke')}
        onCancel={() => setRevoking(null)}
        okButtonProps={{ danger: true, disabled: !reason.trim(), loading: revoke.isPending }}
        onOk={() => revoking && revoke.mutate({ id: revoking.id, reason })}
        destroyOnHidden
      >
        <Input.TextArea rows={3} placeholder="Reason" value={reason} onChange={(e) => setReason(e.target.value)} />
      </Modal>
    </Space>
  );
}

export default function NetworkProfile() {
  const { t } = useTranslation();
  return (
    <PageContainer title={t('nav.networkProfile')}>
      <Tabs
        items={[
          { key: 'profile', label: 'Public profile', children: <ProfileForm /> },
          { key: 'credentials', label: 'Credentials', children: <Credentials /> },
        ]}
      />
    </PageContainer>
  );
}
