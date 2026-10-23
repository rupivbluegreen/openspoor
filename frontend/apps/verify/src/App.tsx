import { lazy, Suspense } from 'react';
import { Flex, Layout, Segmented, Spin, theme, Typography } from 'antd';
import { CodeOutlined, ScanOutlined } from '@ant-design/icons';
import { createBrowserRouter, Outlet, RouterProvider, useLocation, useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
import { LanguageSwitch, Logo, ThemeToggle } from '@openspoor/ui';

const Scan = lazy(() => import('./Scan'));
const Playground = lazy(() => import('./Playground'));
const Unsubscribe = lazy(() => import('./Unsubscribe'));

function Frame() {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  const loc = useLocation();
  const nav = useNavigate();
  const current = loc.pathname.startsWith('/playground') ? '/playground' : '/';
  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Layout.Header style={{ background: token.colorBgContainer, padding: '0 12px', borderBottom: `1px solid ${token.colorBorderSecondary}`, height: 'auto', lineHeight: 'normal' }}>
        <Flex align="center" justify="space-between" gap={8} wrap style={{ minHeight: 56, maxWidth: 1200, margin: '0 auto' }}>
          <Flex align="center" gap={8}>
            <Logo />
            <Typography.Text strong>{t('app.name')}</Typography.Text>
          </Flex>
          <Segmented
            value={current}
            onChange={(v) => nav(String(v))}
            options={[
              { value: '/', label: t('nav.verify'), icon: <ScanOutlined /> },
              { value: '/playground', label: 'Playground', icon: <CodeOutlined /> },
            ]}
          />
          <Flex>
            <LanguageSwitch />
            <ThemeToggle />
          </Flex>
        </Flex>
      </Layout.Header>
      <Layout.Content style={{ padding: 16 }}>
        <Suspense fallback={<Flex justify="center" style={{ padding: 48 }}><Spin size="large" /></Flex>}>
          <Outlet />
        </Suspense>
      </Layout.Content>
      <Layout.Footer style={{ textAlign: 'center' }}>
        <Typography.Text type="secondary">{t('app.name')} · Apache-2.0</Typography.Text>
      </Layout.Footer>
    </Layout>
  );
}

const router = createBrowserRouter([
  {
    Component: Frame,
    children: [
      { path: '/', Component: Scan },
      { path: '/playground', Component: Playground },
      { path: '/unsubscribe', Component: Unsubscribe },
      { path: '*', Component: Scan },
    ],
  },
], { basename: import.meta.env.BASE_URL.replace(/\/$/, '') || '/' });

export function App() {
  return <RouterProvider router={router} />;
}
