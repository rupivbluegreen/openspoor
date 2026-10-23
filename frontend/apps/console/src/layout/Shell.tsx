import type { ReactNode } from 'react';
import { Dropdown, Flex, Select, Tag, Typography } from 'antd';
import { LogoutOutlined, SwapOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { Link, useLocation, useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, notifyError, LanguageSwitch, Logo, ProLayout, TableViewsScope, ThemeToggle, type MenuDataItem, type Tenant, type TokenResponse } from '@openspoor/ui/pro';
import { useSession } from '../lib/session';
import { NAV } from '../nav';
import { GlobalSearch } from './GlobalSearch';

function TenantSwitcher() {
  const { t } = useTranslation();
  const { tenant, principal, signIn } = useSession();
  const nav = useNavigate();
  const tenants = useQuery({
    queryKey: ['me', 'tenants'],
    queryFn: () => api.list<Tenant>('/me/tenants'),
    enabled: principal?.kind === 'user',
    meta: { silent: true },
  });
  const items = tenants.data?.items ?? (tenant ? [tenant] : []);
  return (
    <Select
      size="small"
      variant="borderless"
      aria-label={t('action.switchTenant')}
      suffixIcon={<SwapOutlined />}
      style={{ minWidth: 160 }}
      value={tenant?.id}
      loading={tenants.isLoading}
      popupMatchSelectWidth={false}
      options={items.map((x) => ({ value: x.id, label: x.name || x.slug }))}
      onChange={async (tenantId: string) => {
        if (tenantId === tenant?.id) return;
        try {
          const res = await api.post<TokenResponse>('/auth/switch', { tenantId });
          signIn(res);
          nav('/', { replace: true });
        } catch (e) {
          notifyError(e);
        }
      }}
    />
  );
}

export function Shell({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const loc = useLocation();
  const { principal, tenant, isFood, signOut } = useSession();

  const route: MenuDataItem = {
    path: '/',
    children: NAV.filter((g) => !g.foodOnly || isFood).map((g) => ({
      path: `/${g.key}`,
      name: t(`pillar.${g.key}`),
      icon: g.icon,
      children: g.items.map((i) => ({ path: i.path, name: t(`nav.${i.label}`), icon: i.icon })),
    })),
  };

  return (
    <ProLayout
      title={t('app.name')}
      logo={<Logo />}
      layout="mix"
      splitMenus={false}
      fixSiderbar
      fixedHeader
      location={{ pathname: loc.pathname }}
      route={route}
      menu={{ locale: false, defaultOpenAll: false }}
      siderWidth={232}
      menuItemRender={(item, dom) => (item.path ? <Link to={item.path}>{dom}</Link> : dom)}
      actionsRender={(props) => [
        <GlobalSearch key="search" compact={props.isMobile} />,
        !props.isMobile && tenant?.plan ? <Tag key="plan" color="green">{tenant.plan}</Tag> : null,
        <TenantSwitcher key="tenant" />,
        <LanguageSwitch key="lang" />,
        <ThemeToggle key="theme" />,
      ]}
      avatarProps={{
        size: 'small',
        title: principal?.name || principal?.email || '…',
        render: (_p, dom) => (
          <Dropdown
            menu={{
              items: [
                {
                  key: 'who',
                  disabled: true,
                  label: (
                    <Flex vertical>
                      <Typography.Text strong>{principal?.email}</Typography.Text>
                      <Typography.Text type="secondary">{principal?.roles?.join(', ')}</Typography.Text>
                    </Flex>
                  ),
                },
                { type: 'divider' },
                { key: 'logout', icon: <LogoutOutlined />, label: t('action.signOut'), onClick: signOut },
              ],
            }}
          >
            {dom}
          </Dropdown>
        ),
      }}
    >
      <TableViewsScope scope={principal ? `${principal.tenantId}.${principal.userId}` : undefined}>{children}</TableViewsScope>
    </ProLayout>
  );
}
