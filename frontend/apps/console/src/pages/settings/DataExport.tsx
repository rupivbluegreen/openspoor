import { useState } from 'react';
import { Alert, Button, Card, Space, Typography } from 'antd';
import { CloudDownloadOutlined, DeleteOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { api, downloadBlob, notifyError, PageContainer, useESignature } from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';

export default function DataExport() {
  const { t } = useTranslation();
  const { tenant, hasRole } = useSession();
  const [busy, setBusy] = useState(false);
  const { requestSignature, holder } = useESignature();

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

  const deleteTenant = async () => {
    if (!tenant) return;
    const sig = await requestSignature({ entity: 'tenant', entityId: tenant.id, defaultMeaning: 'responsibility', defaultReason: 'Delete tenant and all data' });
    if (!sig) return;
    try {
      await api.del('/tenant', { json: { signatureId: sig.id } });
    } catch (e) {
      notifyError(e);
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
            <Button danger icon={<DeleteOutlined />} onClick={deleteTenant}>
              {t('action.delete')}
            </Button>
          </Card>
        )}
      </Space>
      {holder}
    </PageContainer>
  );
}
