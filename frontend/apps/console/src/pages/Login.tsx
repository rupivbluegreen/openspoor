import { Button, Divider, Tooltip } from 'antd';
import { LoginOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, LoginCard, type Meta } from '@openspoor/ui/pro';
import { useSession } from '../lib/session';

export default function Login() {
  const { t } = useTranslation();
  const { token, signIn } = useSession();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const next = params.get('next') || '/';
  const meta = useQuery({ queryKey: ['meta'], queryFn: () => api.get<Meta>('/meta', undefined, { anonymous: true }), meta: { silent: true } });

  if (token) return <Navigate to={next} replace />;
  return (
    <LoginCard
      title={t('auth.title')}
      defaultTenant={params.get('tenant') ?? undefined}
      onLoggedIn={(res) => {
        signIn(res);
        nav(next, { replace: true });
      }}
      extra={
        <>
          <div style={{ textAlign: 'center', marginTop: 12 }}>
            <Link to="/reset-password">{t('security.forgot')}</Link>
          </div>
          {meta.data?.oidc ? (
          <>
            <Divider plain>SSO</Divider>
            {/* Placeholder: the OIDC redirect flow is not part of the v1 API contract yet. */}
            <Tooltip title="OIDC is enabled on this node">
              <Button block icon={<LoginOutlined />} href="#sso" onClick={(e) => e.preventDefault()}>
                {t('action.signInSso')}
              </Button>
            </Tooltip>
          </>
          ) : null}
        </>
      }
    />
  );
}
