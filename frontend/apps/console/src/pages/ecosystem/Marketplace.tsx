import { useMemo, useState } from 'react';
import { Alert, Button, Card, Col, Empty, Modal, Popconfirm, Row, Segmented, Space, Tag, Tooltip, Typography } from 'antd';
import { CheckCircleOutlined, DownloadOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, CodeView, formatDateTime, PageContainer, type AppInstall, type AppInstallResult, type AppListing } from '@openspoor/ui/pro';
import { useAction } from '../../lib/hooks';
import { useSession } from '../../lib/session';

const KINDS = ['all', 'connector', 'agent', 'rulepack', 'integration', 'app'] as const;
const INVALIDATE = [['/marketplace'], ['/apps/installed'], ['/apps']] as const;

function AppCard({ app, installedAt, canManage, onInstalled }: { app: AppListing; installedAt?: AppInstall; canManage: boolean; onInstalled: (r: AppInstallResult) => void }) {
  const { t } = useTranslation();
  const install = useAction(() => api.post<AppInstallResult>(`/apps/${encodeURIComponent(app.id)}/install`), { invalidate: INVALIDATE, onSuccess: onInstalled });
  const uninstall = useAction(() => api.del(`/apps/${encodeURIComponent(app.id)}/install`), { invalidate: INVALIDATE });
  const installed = !!installedAt;

  const actions = !canManage
    ? [<Tooltip key="ro" title="Admin role required"><Typography.Text type="secondary">{installed ? 'Installed' : 'Not installed'}</Typography.Text></Tooltip>]
    : installed
      ? [
          <Popconfirm key="rm" title={`${t('action.remove')} ${app.name}?`} description="The app's API key is revoked. Audited." onConfirm={() => uninstall.mutateAsync(undefined)}>
            <Button type="link" danger loading={uninstall.isPending}>
              {t('action.remove')}
            </Button>
          </Popconfirm>,
        ]
      : [
          <Popconfirm
            key="in"
            title={`Install ${app.name}?`}
            description={app.roles?.length ? `Grants an API key with: ${app.roles.join(', ')}` : 'Grants an API key with no roles.'}
            onConfirm={() => install.mutateAsync(undefined)}
          >
            <Button type="link" icon={<DownloadOutlined />} loading={install.isPending}>
              Install
            </Button>
          </Popconfirm>,
        ];

  return (
    <Card
      title={
        <Space>
          {app.name}
          {app.verified && (
            <Tooltip title="Verified package">
              <SafetyCertificateOutlined style={{ color: 'var(--ant-color-success)' }} />
            </Tooltip>
          )}
        </Space>
      }
      extra={installed && <Tag icon={<CheckCircleOutlined />} color="success">installed</Tag>}
      actions={actions}
      style={{ height: '100%', display: 'flex', flexDirection: 'column' }}
      styles={{ body: { flex: 1 } }}
    >
      <Space direction="vertical" size={8} style={{ width: '100%' }}>
        <Space wrap size={4}>
          <Tag color="blue">{app.kind}</Tag>
          <Tag>v{app.version}</Tag>
          {!app.verified && <Tag color="warning">community</Tag>}
        </Space>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }} ellipsis={{ rows: 3 }}>
          {app.description || '–'}
        </Typography.Paragraph>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {app.publisher}
          {installedAt && ` · installed ${formatDateTime(installedAt.createdAt)} by ${installedAt.installedBy}`}
        </Typography.Text>
        {!!app.roles?.length && (
          <div>
            {app.roles.map((r) => (
              <Tag key={r} bordered={false}>
                {r}
              </Tag>
            ))}
          </div>
        )}
        {app.homepage && (
          <Typography.Link href={/^https:\/\//i.test(app.homepage) ? app.homepage : undefined} target="_blank" rel="noreferrer">
            {app.homepage}
          </Typography.Link>
        )}
      </Space>
    </Card>
  );
}

export default function Marketplace() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const [kind, setKind] = useState<string>('all');
  const [result, setResult] = useState<AppInstallResult | null>(null);
  const catalogue = useQuery({
    queryKey: ['/marketplace', kind],
    queryFn: () => api.list<AppListing>('/marketplace', kind === 'all' ? undefined : { kind }),
  });
  const installed = useQuery({ queryKey: ['/apps/installed'], queryFn: () => api.list<AppInstall>('/apps/installed') });
  const byId = useMemo(() => new Map((installed.data?.items ?? []).map((i) => [i.appId, i])), [installed.data]);
  const canManage = hasRole('Admin');
  const items = catalogue.data?.items ?? [];

  return (
    <PageContainer
      title={t('nav.marketplace')}
      subTitle={`${byId.size} installed`}
      extra={<Segmented value={kind} onChange={(v) => setKind(String(v))} options={KINDS.map((k) => ({ value: k, label: k === 'all' ? t('common.all') : k }))} />}
    >
      {!canManage && <Alert type="info" showIcon title="Installing and removing apps requires the Admin role." style={{ marginBottom: 16 }} />}
      {catalogue.isLoading ? (
        <Card loading />
      ) : !items.length ? (
        <Card>
          <Empty description={t('common.empty')} />
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {items.map((a) => (
            <Col key={a.id} xs={24} md={12} xl={8}>
              <AppCard app={a} installedAt={byId.get(a.id)} canManage={canManage} onInstalled={setResult} />
            </Col>
          ))}
        </Row>
      )}
      <Modal
        open={!!result}
        title={result ? `Installed ${result.app.name}` : ''}
        onCancel={() => setResult(null)}
        onOk={() => setResult(null)}
        cancelButtonProps={{ style: { display: 'none' } }}
        maskClosable={false}
      >
        <Alert type="warning" showIcon title={t('common.secretOnce')} description={result?.note} style={{ marginBottom: 12 }} />
        <CodeView code={result?.apiKey ?? ''} />
      </Modal>
    </PageContainer>
  );
}
