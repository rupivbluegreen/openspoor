import { Card, Flex, Typography } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, isHexColor, Logo, PageContainer, ProForm, ProFormColorPicker, ProFormText, useAppSettings, type Brand } from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';

export default function Branding() {
  const { t } = useTranslation();
  const { setBrandColor } = useAppSettings();
  const q = useQuery({ queryKey: ['/tenant/branding'], queryFn: () => api.get<Brand>('/tenant/branding') });
  const save = useSubmit(
    (v: { primaryColor?: unknown; logoUrl?: string }) => {
      const color = typeof v.primaryColor === 'string' ? v.primaryColor : (v.primaryColor as { toHexString?: () => string } | undefined)?.toHexString?.();
      return api.put<Brand>('/tenant/branding', { primaryColor: color, logoUrl: v.logoUrl || undefined });
    },
    { invalidate: [['/tenant/branding'], ['tenant']], success: t('common.saved'), onSuccess: (b) => isHexColor(b?.primaryColor) && setBrandColor(b.primaryColor) },
  );
  return (
    <PageContainer title={t('nav.branding')}>
      <Card loading={q.isLoading} style={{ maxWidth: 640 }}>
        {!q.isLoading && (
          <ProForm initialValues={q.data ?? {}} onFinish={save} submitter={{ searchConfig: { submitText: t('action.save') } }}>
            <ProFormColorPicker name="primaryColor" label="Primary colour" />
            <ProFormText name="logoUrl" label="Logo URL" rules={[{ type: 'url' }]} />
            <Flex align="center" gap={12} style={{ marginBottom: 16 }}>
              {q.data?.logoUrl ? <img src={q.data.logoUrl} alt="logo" style={{ height: 32 }} /> : <Logo />}
              <Typography.Text type="secondary">{t('action.preview')}</Typography.Text>
            </Flex>
          </ProForm>
        )}
      </Card>
    </PageContainer>
  );
}
