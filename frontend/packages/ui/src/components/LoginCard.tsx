import { useState, type ReactNode } from 'react';
import { Card, Flex, Typography } from 'antd';
import { KeyOutlined, LockOutlined, MailOutlined, ShopOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { ProForm, ProFormText } from '../pro';
import { api } from '../api/client';
import { ApiError } from '../api/errors';
import type { TokenResponse } from '../api/types';
import { LanguageSwitch, ThemeToggle } from './Toggles';
import { Logo } from './Logo';
import { notifyError } from '../notify';

export interface LoginCardProps {
  title: ReactNode;
  onLoggedIn: (res: TokenResponse) => void | Promise<void>;
  extra?: ReactNode;
  defaultTenant?: string;
}

/** Tenant slug + email + password → POST /auth/login. */
export function LoginCard({ title, onLoggedIn, extra, defaultTenant }: LoginCardProps) {
  const { t } = useTranslation();
  // The server asks for a second factor only after the password was right.
  const [needCode, setNeedCode] = useState(false);
  return (
    <Flex vertical align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }}>
      <Flex gap={4} style={{ position: 'absolute', top: 12, insetInlineEnd: 12 }}>
        <LanguageSwitch />
        <ThemeToggle />
      </Flex>
      <Card style={{ width: '100%', maxWidth: 400 }}>
        <Flex vertical align="center" gap={4} style={{ marginBottom: 24 }}>
          <Logo size={40} />
          <Typography.Title level={4} style={{ margin: '8px 0 0' }}>
            {title}
          </Typography.Title>
          <Typography.Text type="secondary">{t('auth.subtitle')}</Typography.Text>
        </Flex>
        <ProForm<{ tenant: string; email: string; password: string; mfaCode?: string }>
          initialValues={{ tenant: defaultTenant }}
          submitter={{
            searchConfig: { submitText: t('action.signIn') },
            resetButtonProps: false,
            submitButtonProps: { block: true, size: 'large' },
            render: (_p, dom) => dom.pop(),
          }}
          onFinish={async (v) => {
            try {
              const res = await api.post<TokenResponse>('/auth/login', v, { anonymous: true });
              await onLoggedIn(res);
              return true;
            } catch (e) {
              if (e instanceof ApiError && e.status === 401 && (e.problem?.errors as { mfaRequired?: boolean } | undefined)?.mfaRequired) {
                setNeedCode(true);
                return false;
              }
              notifyError(e);
              return false;
            }
          }}
        >
          <ProFormText name="tenant" label={t('auth.tenantSlug')} fieldProps={{ prefix: <ShopOutlined />, autoComplete: 'organization' }} rules={[{ required: true }]} />
          <ProFormText name="email" label={t('field.email')} fieldProps={{ prefix: <MailOutlined />, autoComplete: 'username' }} rules={[{ required: true }, { type: 'email' }]} />
          <ProFormText.Password name="password" label={t('field.password')} fieldProps={{ prefix: <LockOutlined />, autoComplete: 'current-password' }} rules={[{ required: true }]} />
          {needCode && (
            <ProFormText name="mfaCode" label={t('security.signInCode')} fieldProps={{ prefix: <KeyOutlined />, autoComplete: 'one-time-code', autoFocus: true }} rules={[{ required: true }]} />
          )}
        </ProForm>
        {extra}
      </Card>
    </Flex>
  );
}
