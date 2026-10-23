import { useState } from 'react';
import { Button, Card, Flex, Form, Input, Typography } from 'antd';
import { Link, useSearchParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, Logo, notifyError } from '@openspoor/ui/pro';

/** Public page: without a token it asks for the recovery e-mail, with the token from that e-mail it sets a new password. */
export default function ResetPassword() {
  const { t } = useTranslation();
  const [params] = useSearchParams();
  const token = params.get('token');
  const [done, setDone] = useState<'sent' | 'reset' | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (fn: () => Promise<unknown>, outcome: 'sent' | 'reset') => {
    setBusy(true);
    try {
      await fn();
      setDone(outcome);
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Flex vertical align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }}>
      <Card style={{ width: '100%', maxWidth: 400 }}>
        <Flex vertical align="center" gap={4} style={{ marginBottom: 16 }}>
          <Logo size={40} />
          <Typography.Title level={4} style={{ margin: '8px 0 0' }}>
            {token ? t('security.resetTitle') : t('security.forgot')}
          </Typography.Title>
        </Flex>
        {done === 'sent' && <Typography.Paragraph>{t('security.forgotSent')}</Typography.Paragraph>}
        {done === 'reset' && <Typography.Paragraph>{t('security.resetDone')}</Typography.Paragraph>}
        {!done && !token && (
          <Form layout="vertical" onFinish={(v: { tenant: string; email: string }) => submit(() => api.post('/auth/password/forgot', v, { anonymous: true }), 'sent')}>
            <Typography.Paragraph type="secondary">{t('security.forgotIntro')}</Typography.Paragraph>
            <Form.Item name="tenant" label={t('auth.tenantSlug')} rules={[{ required: true }]}>
              <Input autoComplete="organization" />
            </Form.Item>
            <Form.Item name="email" label={t('field.email')} rules={[{ required: true }, { type: 'email' }]}>
              <Input autoComplete="username" />
            </Form.Item>
            <Button type="primary" htmlType="submit" block loading={busy}>
              {t('security.sendLink')}
            </Button>
          </Form>
        )}
        {!done && token && (
          <Form layout="vertical" onFinish={(v: { newPassword: string }) => submit(() => api.post('/auth/password/reset', { token, newPassword: v.newPassword }, { anonymous: true }), 'reset')}>
            <Form.Item name="newPassword" label={t('security.newPassword')} rules={[{ required: true }]}>
              <Input.Password autoComplete="new-password" />
            </Form.Item>
            <Form.Item
              name="confirm"
              label={t('security.confirmPassword')}
              dependencies={['newPassword']}
              rules={[
                { required: true },
                ({ getFieldValue }) => ({
                  validator: (_r, value) => (!value || value === getFieldValue('newPassword') ? Promise.resolve() : Promise.reject(new Error(t('security.passwordMismatch')))),
                }),
              ]}
            >
              <Input.Password autoComplete="new-password" />
            </Form.Item>
            <Button type="primary" htmlType="submit" block loading={busy}>
              {t('action.save')}
            </Button>
          </Form>
        )}
        <div style={{ textAlign: 'center', marginTop: 12 }}>
          <Link to="/login">{t('security.backToSignIn')}</Link>
        </div>
      </Card>
    </Flex>
  );
}
