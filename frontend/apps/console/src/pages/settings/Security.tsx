import { useState } from 'react';
import { Alert, Button, Card, Form, Input, Space, Switch, Tag, Typography } from 'antd';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, notifyError, notifySuccess, PageContainer, tokenStore, type TokenResponse } from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';

interface MfaStatus {
  enabled: boolean;
  pending: boolean;
  recoveryCodesRemaining: number;
  requiredByPolicy: boolean;
}
interface Enrolment {
  secret: string;
  otpauthUri: string;
}
type WithCodes = TokenResponse & { recoveryCodes?: string[] };

function RecoveryCodes({ codes }: { codes: string[] }) {
  const { t } = useTranslation();
  return (
    <Alert
      type="warning"
      showIcon
      title={t('security.recoveryTitle')}
      description={
        <>
          <Typography.Paragraph>{t('security.recoveryHint')}</Typography.Paragraph>
          <Typography.Paragraph copyable={{ text: codes.join('\n') }} code style={{ whiteSpace: 'pre-line' }}>
            {codes.join('\n')}
          </Typography.Paragraph>
        </>
      }
    />
  );
}

function PasswordCard() {
  const { t } = useTranslation();
  const [form] = Form.useForm<{ currentPassword: string; newPassword: string; confirm: string }>();
  const [busy, setBusy] = useState(false);
  const submit = async (v: { currentPassword: string; newPassword: string }) => {
    setBusy(true);
    try {
      // The server ends every other session and returns a fresh token for this one.
      const res = await api.post<TokenResponse>('/auth/password', { currentPassword: v.currentPassword, newPassword: v.newPassword });
      tokenStore.set(res.token);
      form.resetFields();
      notifySuccess(t('security.passwordChanged'));
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title={t('security.passwordTitle')}>
      <Form form={form} layout="vertical" onFinish={submit} style={{ maxWidth: 420 }}>
        <Form.Item name="currentPassword" label={t('security.currentPassword')} rules={[{ required: true }]}>
          <Input.Password autoComplete="current-password" />
        </Form.Item>
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
        <Button type="primary" htmlType="submit" loading={busy}>
          {t('action.save')}
        </Button>
      </Form>
    </Card>
  );
}

function MfaCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const status = useQuery({ queryKey: ['/auth/mfa'], queryFn: () => api.get<MfaStatus>('/auth/mfa') });
  const [enrol, setEnrol] = useState<Enrolment | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [dform] = Form.useForm<{ password: string; code: string }>();
  const s = status.data;

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    try {
      await fn();
      await qc.invalidateQueries({ queryKey: ['/auth/mfa'] });
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy(false);
    }
  };
  // Changing the second factor ends the user's other sessions; keep this one with the token the server returns.
  const adopt = (res: WithCodes) => {
    tokenStore.set(res.token);
    setCodes(res.recoveryCodes ?? null);
  };

  return (
    <Card
      title={t('security.mfaTitle')}
      extra={s ? <Tag color={s.enabled ? 'success' : s.pending ? 'warning' : 'default'}>{s.enabled ? t('security.mfaOn') : s.pending ? t('security.mfaPending') : t('security.mfaOff')}</Tag> : null}
    >
      <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 520 }}>
        {codes && <RecoveryCodes codes={codes} />}
        {s && !s.enabled && !enrol && (
          <>
            <Typography.Text type="secondary">{t('security.mfaIntro')}</Typography.Text>
            <Button type="primary" loading={busy} onClick={() => run(async () => setEnrol(await api.post<Enrolment>('/auth/mfa/enrol')))}>
              {t('security.mfaSetup')}
            </Button>
          </>
        )}
        {s && !s.enabled && enrol && (
          <Form layout="vertical" onFinish={(v: { code: string }) => run(async () => { adopt(await api.post<WithCodes>('/auth/mfa/confirm', { code: v.code })); setEnrol(null); })}>
            <Typography.Paragraph>{t('security.mfaScan')}</Typography.Paragraph>
            <Typography.Paragraph>
              <Typography.Text type="secondary">{t('security.mfaSecret')}: </Typography.Text>
              <Typography.Text code copyable>{enrol.secret}</Typography.Text>
            </Typography.Paragraph>
            <Typography.Paragraph>
              <Typography.Text type="secondary">{t('security.mfaUri')}: </Typography.Text>
              <Typography.Text copyable={{ text: enrol.otpauthUri }} ellipsis style={{ maxWidth: 360 }}>{enrol.otpauthUri}</Typography.Text>
            </Typography.Paragraph>
            <Form.Item name="code" label={t('security.mfaCode')} rules={[{ required: true }]}>
              <Input inputMode="numeric" autoComplete="one-time-code" maxLength={8} />
            </Form.Item>
            <Button type="primary" htmlType="submit" loading={busy}>
              {t('security.mfaConfirm')}
            </Button>
          </Form>
        )}
        {s?.enabled && (
          <>
            <Typography.Text>{t('security.mfaEnabled')} {t('security.recoveryLeft', { n: s.recoveryCodesRemaining })}</Typography.Text>
            <Form form={dform} layout="vertical" onFinish={(v: { password: string; code: string }) => run(async () => adopt(await api.post<WithCodes>('/auth/mfa/disable', v)))}>
              <Typography.Paragraph type="secondary">{t('security.mfaDisableHint')}</Typography.Paragraph>
              <Form.Item name="password" label={t('field.password')} rules={[{ required: true }]}>
                <Input.Password autoComplete="current-password" />
              </Form.Item>
              <Form.Item name="code" label={t('security.codeOrRecovery')} rules={[{ required: true }]}>
                <Input autoComplete="one-time-code" />
              </Form.Item>
              <Space>
                <Button danger htmlType="submit" loading={busy} disabled={s.requiredByPolicy}>
                  {t('security.mfaDisable')}
                </Button>
                <Button
                  loading={busy}
                  onClick={() => {
                    const v = dform.getFieldsValue();
                    void run(async () => setCodes((await api.post<{ recoveryCodes: string[] }>('/auth/mfa/recovery-codes', { password: v.password ?? '', code: v.code ?? '' })).recoveryCodes));
                  }}
                >
                  {t('security.regenerate')}
                </Button>
              </Space>
            </Form>
          </>
        )}
      </Space>
    </Card>
  );
}

function PolicyCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const policy = useQuery({ queryKey: ['/tenant/security'], queryFn: () => api.get<{ requireMfaForAdmin: boolean }>('/tenant/security') });
  const [busy, setBusy] = useState(false);
  return (
    <Card title={t('security.policyTitle')}>
      <Space align="start">
        <Switch
          checked={policy.data?.requireMfaForAdmin ?? false}
          loading={busy || policy.isLoading}
          onChange={async (on) => {
            setBusy(true);
            try {
              await api.put('/tenant/security', { requireMfaForAdmin: on });
              // Turning it on ends the sessions of administrators without a second factor, this one excepted.
              await qc.invalidateQueries({ queryKey: ['/tenant/security'] });
            } catch (e) {
              notifyError(e);
            } finally {
              setBusy(false);
            }
          }}
        />
        <div>
          <div>{t('security.requireAdminMfa')}</div>
          <Typography.Text type="secondary">{t('security.policyHint')}</Typography.Text>
        </div>
      </Space>
    </Card>
  );
}

export default function Security() {
  const { t } = useTranslation();
  const { principal, hasRole } = useSession();
  return (
    <PageContainer title={t('nav.security')}>
      <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 720 }}>
        {principal?.mfaSetupRequired && <Alert type="warning" showIcon title={t('security.setupRequired')} />}
        {principal?.kind === 'user' && <PasswordCard />}
        {principal?.kind === 'user' && <MfaCard />}
        {hasRole('Admin') && <PolicyCard />}
      </Space>
    </PageContainer>
  );
}
