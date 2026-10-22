import { useState } from 'react';
import { Alert, Modal, Progress, Space, Tag, Typography } from 'antd';
import { AuditOutlined, EditOutlined, UserSwitchOutlined } from '@ant-design/icons';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, ModalForm, notifySuccess, PageContainer, prettyJson, ProFormDigit, ProFormGroup, ProFormSelect, ProFormTextArea,
  QueryTable, StatusTag, type OperatorTenant,
} from '@openspoor/ui/pro';

type Row = OperatorTenant & Record<string, unknown>;
const PLANS = ['community', 'starter', 'growth', 'enterprise'];
const STATUSES = ['active', 'suspended', 'disabled'];
const QUOTA_KEYS = ['events', 'reports', 'activePartners', 'users', 'storageGb'];

function num(v: unknown): number | undefined {
  return typeof v === 'number' ? v : undefined;
}

function UsageCell({ r }: { r: OperatorTenant }) {
  const usage = (r.usage ?? {}) as Record<string, unknown>;
  const quota = { ...((usage.quota as Record<string, number>) ?? {}), ...(r.quotas ?? {}) };
  const events = num(usage.events);
  if (events === undefined) return <>–</>;
  const max = quota.events;
  return (
    <Space direction="vertical" size={0} style={{ minWidth: 140 }}>
      <Typography.Text>
        {events.toLocaleString()} {max ? `/ ${max.toLocaleString()}` : ''} events
      </Typography.Text>
      {max ? <Progress size="small" percent={Math.min(100, Math.round((events / max) * 100))} showInfo={false} status={events > max ? 'exception' : 'normal'} /> : null}
    </Space>
  );
}

function health(h: OperatorTenant['health']) {
  if (!h) return '–';
  if (typeof h === 'string') return <StatusTag status={h} />;
  return <StatusTag status={String((h as { status?: string }).status ?? 'unknown')} />;
}

export default function Tenants() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [session, setSession] = useState<unknown>(null);
  const patch = useMutation({
    mutationFn: ({ id, body }: { id: string; body: unknown }) => api.patch(`/operator/tenants/${id}`, body),
    onSuccess: () => {
      notifySuccess(t('common.saved'));
      void qc.invalidateQueries({ queryKey: ['/operator/tenants'] });
    },
  });
  const impersonate = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) => api.post(`/operator/tenants/${id}/impersonate`, { reason }),
    onSuccess: (res) => {
      notifySuccess(t('operator.impersonated'));
      setSession(res ?? {});
    },
  });
  const audited = <Alert type="info" showIcon icon={<AuditOutlined />} title={t('common.audited')} style={{ marginBottom: 16 }} />;

  return (
    <PageContainer title={t('operator.tenants')} content={audited}>
      <QueryTable<Row>
        rowKey="id"
        queryKey={['/operator/tenants']}
        search={{ labelWidth: 'auto' }}
        fetcher={async (p) => {
          const page = await api.list<Row>('/operator/tenants');
          const q = String(p.name ?? '').toLowerCase();
          const items = page.items.filter(
            (x) => (!q || `${x.name} ${x.slug}`.toLowerCase().includes(q)) && (!p.plan || x.plan === p.plan) && (!p.status || x.status === p.status),
          );
          return { items, total: items.length };
        }}
        columns={[
          { title: t('field.name'), dataIndex: 'name', render: (_d, r) => <Space direction="vertical" size={0}><strong>{r.name}</strong><Typography.Text type="secondary">{r.slug}</Typography.Text></Space> },
          { title: t('field.plan'), dataIndex: 'plan', valueType: 'select', valueEnum: Object.fromEntries(PLANS.map((p) => [p, { text: p }])), render: (_d, r) => <Tag color="green">{r.plan}</Tag> },
          { title: t('field.status'), dataIndex: 'status', valueType: 'select', valueEnum: Object.fromEntries(STATUSES.map((s) => [s, { text: s }])), render: (_d, r) => <StatusTag status={r.status} /> },
          { title: 'Region', dataIndex: 'region', search: false },
          { title: 'Verticals', dataIndex: 'verticals', search: false, render: (_d, r) => r.verticals?.map((v) => <Tag key={v}>{v}</Tag>) },
          { title: t('field.usage'), search: false, render: (_d, r) => <UsageCell r={r} /> },
          { title: t('field.health'), dataIndex: 'health', search: false, render: (_d, r) => health(r.health) },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'date', search: false },
          {
            title: t('common.actions'),
            valueType: 'option',
            render: (_d, r) => [
              <ModalForm<{ plan: string; status: string; quotas: Record<string, number> }>
                key="edit"
                title={`${t('operator.editTenant')} · ${r.name}`}
                trigger={<a><EditOutlined /> {t('action.edit')}</a>}
                modalProps={{ destroyOnHidden: true }}
                initialValues={{ plan: r.plan, status: r.status, quotas: { ...(((r.usage as Record<string, unknown> | undefined)?.quota as object) ?? {}), ...(r.quotas ?? {}) } }}
                onFinish={async (v) => {
                  try {
                    const quotas = Object.fromEntries(Object.entries(v.quotas ?? {}).filter(([, x]) => x !== undefined && x !== null));
                    await patch.mutateAsync({ id: r.id, body: { plan: v.plan, status: v.status, quotas } });
                    return true;
                  } catch {
                    return false;
                  }
                }}
              >
                {audited}
                <ProFormSelect name="plan" label={t('field.plan')} options={PLANS} rules={[{ required: true }]} />
                <ProFormSelect name="status" label={t('field.status')} options={STATUSES} rules={[{ required: true }]} />
                <ProFormGroup title={t('operator.quotas')}>
                  {QUOTA_KEYS.map((k) => (
                    <ProFormDigit key={k} name={['quotas', k]} label={k} width="sm" min={0} fieldProps={{ precision: 0 }} />
                  ))}
                </ProFormGroup>
              </ModalForm>,
              <ModalForm<{ reason: string }>
                key="imp"
                title={`${t('action.impersonate')} · ${r.name}`}
                trigger={<a><UserSwitchOutlined /> {t('action.impersonate')}</a>}
                modalProps={{ destroyOnHidden: true }}
                submitter={{ searchConfig: { submitText: t('action.impersonate') } }}
                onFinish={async (v) => {
                  try {
                    await impersonate.mutateAsync({ id: r.id, reason: v.reason });
                    return true;
                  } catch {
                    return false;
                  }
                }}
              >
                <Alert type="warning" showIcon title="Consented support access" description={t('common.audited')} style={{ marginBottom: 16 }} />
                <ProFormTextArea name="reason" label={t('operator.impersonateReason')} rules={[{ required: true, min: 10 }]} placeholder="Ticket #1234: customer asked us to check their EU FMD connector" />
              </ModalForm>,
            ],
          },
        ]}
      />
      <Modal open={session !== null} onCancel={() => setSession(null)} footer={null} title={t('operator.impersonated')} width={640}>
        <Alert type="info" showIcon title={t('common.audited')} style={{ marginBottom: 12 }} />
        <CodeView code={prettyJson(session)} />
      </Modal>
    </PageContainer>
  );
}
