import { lazy, Suspense, useSyncExternalStore } from 'react';
import { Alert, Flex, Spin } from 'antd';
import { LogoutOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, LanguageSwitch, LoginCard, Logo, notifyError, ProLayout, ThemeToggle, tokenStore, type Principal } from '@openspoor/ui/pro';

const Tenants = lazy(() => import('./Tenants'));

function useToken() {
  return useSyncExternalStore((cb) => tokenStore.subscribe(cb), () => tokenStore.get());
}

export function App() {
  const { t } = useTranslation();
  const token = useToken();
  const qc = useQueryClient();
  const me = useQuery({ queryKey: ['me', token], queryFn: () => api.get<Principal>('/me'), enabled: !!token });

  const signOut = () => {
    tokenStore.set(null);
    qc.clear();
  };

  if (!token) {
    return (
      <LoginCard
        title={t('auth.operatorTitle')}
        onLoggedIn={(res) => {
          if (!res.principal?.operator) {
            notifyError(new Error(t('auth.operatorOnly')));
            return;
          }
          tokenStore.set(res.token);
        }}
      />
    );
  }
  if (me.data && !me.data.operator) {
    return (
      <Flex vertical align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }} gap={16}>
        <Alert type="error" showIcon title={t('auth.operatorOnly')} />
        <a onClick={signOut}>{t('action.signOut')}</a>
      </Flex>
    );
  }

  return (
    <ProLayout
      title={`${t('app.name')} · ${t('app.operator')}`}
      logo={<Logo />}
      layout="top"
      location={{ pathname: '/tenants' }}
      route={{ path: '/', children: [{ path: '/tenants', name: t('nav.tenants') }] }}
      menu={{ locale: false }}
      actionsRender={() => [<LanguageSwitch key="lang" />, <ThemeToggle key="theme" />]}
      avatarProps={{
        size: 'small',
        title: me.data?.email ?? '…',
        render: (_p, dom) => (
          <Flex align="center" gap={8}>
            {dom}
            <a onClick={signOut} aria-label={t('action.signOut')}>
              <LogoutOutlined />
            </a>
          </Flex>
        ),
      }}
    >
      <Suspense fallback={<Spin />}>{me.isLoading ? <Spin /> : <Tenants />}</Suspense>
    </ProLayout>
  );
}
