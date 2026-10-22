import { useState } from 'react';
import { Alert, Button, Card, Space, Typography } from 'antd';
import { CloudDownloadOutlined, DeleteOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { api, downloadBlob, ModalForm, notifyError, PageContainer, ProFormText, ProFormTextArea, tokenStore } from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';

export default function DataExport() {
  const { t } = useTranslation();
  const { tenant, hasRole } = useSession();
  const [busy, setBusy] = useState(false);

  const exportData = async () => {
    setBusy(true);
    try {
      const blob = await api.request<Blob>('POST', '/tenant/export', { json: {}, responseType: 'blob' });
      const ext = blob.type.includes('zip') ? 'zip' : 'json';
      downloadBlob(blob, `openspoor-${tenant?.slug ?? 'tenant'}-export.${ext}`);
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy(false);
    }
  };

  // The server re-authenticates the password and requires the typed slug before crypto-shredding the tenant.
  const deleteTenant = async (v: { confirmSlug: string; password: string; reason: string }) => {
    try {
      await api.del('/tenant', { json: v });
      tokenStore.set(null);
      window.location.assign('/login');
      return true;
    } catch (e) {
      notifyError(e);
      return false;
    }
  };

  return (
    <PageContainer title={t('nav.dataExport')}>
      <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 720 }}>
        <Card title="GDPR export">
          <Typography.Paragraph type="secondary">Download a machine-readable export of all data held for this tenant.</Typography.Paragraph>
          <Button type="primary" icon={<CloudDownloadOutlined />} loading={busy} onClick={exportData}>
            {t('action.export')}
          </Button>
        </Card>
        {hasRole('Admin') && (
          <Card title="Delete tenant">
            <Alert type="error" showIcon title="Deleting the tenant is irreversible and requires an electronic signature." style={{ marginBottom: 12 }} />
            <ModalForm<{ confirmSlug: string; password: string; reason: string }>
              title="Delete tenant"
              trigger={<Button danger icon={<DeleteOutlined />}>{t('action.delete')}</Button>}
              submitter={{ submitButtonProps: { danger: true } }}
              modalProps={{ destroyOnHidden: true }}
              onFinish={deleteTenant}
            >
              <ProFormText
                name="confirmSlug"
                label={`Type the tenant slug "${tenant?.slug ?? ''}" to confirm`}
                rules={[{ required: true }, { validator: (_, v) => (v === tenant?.slug ? Promise.resolve() : Promise.reject(new Error('Slug does not match'))) }]}
              />
              <ProFormText.Password name="password" label="Your password" rules={[{ required: true }]} />
              <ProFormTextArea name="reason" label="Reason" rules={[{ required: true }]} />
            </ModalForm>
          </Card>
        )}
      </Space>
    </PageContainer>
  );
}
