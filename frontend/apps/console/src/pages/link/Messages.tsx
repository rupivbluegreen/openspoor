import { useState } from 'react';
import { Badge, Button, Card, Input, Popconfirm, Select, Space, Table, Tabs, Tag, Tooltip, Typography } from 'antd';
import { DeleteOutlined, PlusOutlined, RedoOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import { api, ModalForm, PageContainer, ProFormDigit, ProFormSelect, ProFormSwitch, StatusTag } from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';
import { usePartnerOptions } from '../../lib/options';
import type { LogEntry, MessageLog, SLA } from './linkTypes';

const STATUSES = ['received', 'queued', 'retrying', 'sent', 'delivered', 'acknowledged', 'failed', 'dead'];
const fmt = (s?: string) => (s ? dayjs(s).format('YYYY-MM-DD HH:mm') : '–');

function MessageTable({ dead }: { dead?: boolean }) {
  const { t } = useTranslation();
  const partners = usePartnerOptions();
  const [kind, setKind] = useState<string>();
  const [direction, setDirection] = useState<string>();
  const [status, setStatus] = useState<string>();
  const [partnerId, setPartnerId] = useState<string>();
  const [q, setQ] = useState('');
  const query = { kind, direction, status: dead ? 'dead' : status, partnerId, q };
  const res = useQuery({ queryKey: ['/messages', query], queryFn: () => api.get<MessageLog>('/messages', query) });
  const retry = useAction((id: string) => api.post(`/messages/${id}/retry`), { invalidate: [['/messages']] });
  return (
    <Card size="small">
      <Space wrap style={{ marginBottom: 12 }}>
        <Select allowClear placeholder="Kind" style={{ width: 140 }} value={kind} onChange={setKind}
          options={[{ value: 'b2b', label: 'B2B' }, { value: 'federation', label: 'Federation' }, { value: 'tits', label: 'TI/TS' }]} />
        <Select allowClear placeholder="Direction" style={{ width: 140 }} value={direction} onChange={setDirection}
          options={['outbound', 'inbound'].map((d) => ({ value: d, label: d }))} />
        {!dead && <Select allowClear placeholder={t('field.status')} style={{ width: 160 }} value={status} onChange={setStatus} options={STATUSES.map((s) => ({ value: s, label: s }))} />}
        <Select allowClear showSearch optionFilterProp="label" placeholder={t('field.partner')} style={{ width: 220 }} value={partnerId} onChange={setPartnerId} options={partners} />
        <Input.Search allowClear placeholder={t('action.search')} onSearch={setQ} style={{ width: 240 }} />
      </Space>
      <Table<LogEntry>
        size="small"
        rowKey="id"
        loading={res.isFetching}
        dataSource={res.data?.items ?? []}
        scroll={{ x: true }}
        columns={[
          { title: 'Kind', dataIndex: 'kind', render: (k: string) => <Tag>{k === 'tits' ? 'TI/TS' : k}</Tag> },
          { title: 'Direction', dataIndex: 'direction' },
          { title: t('field.type'), dataIndex: 'type' },
          { title: 'Reference', dataIndex: 'reference', render: (v: string) => <Typography.Text copyable ellipsis style={{ maxWidth: 200 }}>{v}</Typography.Text> },
          { title: t('field.partner'), dataIndex: 'partnerName', render: (v: string) => v || '–' },
          { title: 'Channel', dataIndex: 'channel' },
          { title: t('field.status'), dataIndex: 'status', render: (s: string) => <StatusTag status={s} /> },
          { title: 'Attempts', dataIndex: 'attempts' },
          {
            title: 'Last error',
            dataIndex: 'lastError',
            render: (e: string) => (e ? <Tooltip title={e}><Typography.Text type="danger" ellipsis style={{ maxWidth: 220 }}>{e}</Typography.Text></Tooltip> : '–'),
          },
          { title: t('field.createdAt'), dataIndex: 'createdAt', render: fmt },
          { title: 'Acknowledged', dataIndex: 'ackedAt', render: fmt },
          {
            title: t('common.actions'),
            render: (_v, r) =>
              r.retryable ? (
                <Button size="small" icon={<RedoOutlined />} loading={retry.isPending && retry.variables === r.id} onClick={() => retry.mutate(r.id)}>
                  Retry now
                </Button>
              ) : null,
          },
        ]}
      />
    </Card>
  );
}

function SLAs() {
  const { t } = useTranslation();
  const partners = usePartnerOptions();
  const res = useQuery({ queryKey: ['/messages/slas'], queryFn: () => api.list<SLA>('/messages/slas') });
  const save = useSubmit((v: { partnerId: string; ackHours: number; enabled: boolean }) => api.put(`/messages/slas/${v.partnerId}`, { ackHours: v.ackHours, enabled: v.enabled }), {
    invalidate: [['/messages/slas']],
  });
  const remove = useAction((id: string) => api.del(`/messages/slas/${id}`), { invalidate: [['/messages/slas']] });
  return (
    <Card
      size="small"
      extra={
        <ModalForm<{ partnerId: string; ackHours: number; enabled: boolean }>
          title="Partner SLA"
          trigger={<Button type="primary" icon={<PlusOutlined />}>{t('action.create')}</Button>}
          modalProps={{ destroyOnHidden: true }}
          initialValues={{ ackHours: 24, enabled: true }}
          onFinish={save}
        >
          <ProFormSelect name="partnerId" label={t('field.partner')} options={partners} rules={[{ required: true }]} showSearch />
          <ProFormDigit name="ackHours" label="Acknowledge within (hours)" min={1} max={720} rules={[{ required: true }]} />
          <ProFormSwitch name="enabled" label="Raise alerts on breach" />
        </ModalForm>
      }
    >
      <Table<SLA>
        size="small"
        rowKey="partnerId"
        loading={res.isFetching}
        dataSource={res.data?.items ?? []}
        columns={[
          { title: t('field.partner'), dataIndex: 'partnerName' },
          { title: 'Acknowledge within', dataIndex: 'ackHours', render: (h: number) => `${h} h` },
          { title: 'Alerts', dataIndex: 'enabled', render: (e: boolean) => <StatusTag status={e ? 'active' : 'disabled'} /> },
          {
            title: t('common.actions'),
            render: (_v, r) => (
              <Popconfirm title={t('action.delete')} onConfirm={() => remove.mutate(r.partnerId)}>
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            ),
          },
        ]}
      />
    </Card>
  );
}

export default function Messages() {
  const { t } = useTranslation();
  const dead = useQuery({ queryKey: ['/messages', { status: 'dead' }], queryFn: () => api.get<MessageLog>('/messages', { status: 'dead', limit: 1 }) });
  return (
    <PageContainer title={t('nav.messages')}>
      <Tabs
        items={[
          { key: 'all', label: 'All messages', children: <MessageTable /> },
          { key: 'dlq', label: <Badge count={dead.data?.counts?.dead ?? 0} offset={[12, 0]} size="small">Dead letters</Badge>, children: <MessageTable dead /> },
          { key: 'sla', label: 'Partner SLAs', children: <SLAs /> },
        ]}
      />
    </PageContainer>
  );
}
