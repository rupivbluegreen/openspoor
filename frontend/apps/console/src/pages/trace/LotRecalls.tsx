import { useState } from 'react';
import { Alert, Button, Descriptions, Drawer, Popconfirm, Space, Table, Tag, Typography } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, downloadBlob, formatDateTime, ModalForm, PageContainer, ProFormSelect, ProFormText, ProFormTextArea, StatusTag,
  type LotRecallRow, type RecallHolderRow,
} from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';
import { useAction, useSubmit } from '../../lib/hooks';

const KEY = ['/trace/recalls'] as const;

function RecallDrawer({ id, onClose }: { id: string | null; onClose: () => void }) {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canWrite = hasRole('Compliance');
  const key = ['/trace/recalls', id] as const;
  const q = useQuery({ queryKey: key, queryFn: () => api.get<LotRecallRow>(`/trace/recalls/${id}`), enabled: !!id });
  const r = q.data;
  const open = r?.status === 'open';
  const useRecallAction = (path: string) => useAction(() => api.post(`/trace/recalls/${id}${path}`, {}), { invalidate: [key, KEY], success: t('lotRecall.updated') });
  const holder = useAction(({ hid, to }: { hid: string; to: 'sent' | 'acknowledge' }) => api.post(`/trace/recalls/${id}/holders/${hid}/${to}`, {}), { invalidate: [key, KEY], success: t('lotRecall.updated') });
  const refresh = useRecallAction('/refresh');
  const notify = useRecallAction('/notify');
  const close = useRecallAction('/close');
  const download = async (format: 'json' | 'html') => {
    const body = await api.get<string>(`/trace/recalls/${id}/report`, { format }, { responseType: 'text', headers: { Accept: format === 'html' ? 'text/html' : 'application/json' } });
    downloadBlob(body, `recall-${r?.gtin}-${r?.lot}.${format}`, format === 'html' ? 'text/html;charset=utf-8' : 'application/json');
  };
  return (
    <Drawer open={!!id} onClose={onClose} title={r?.title ?? id} size="large" loading={q.isLoading}>
      {r && (
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <Alert type="info" showIcon message={t('lotRecall.dataHelper')} description={t('lotRecall.responseTime')} />
          <Descriptions size="small" column={2} bordered>
            <Descriptions.Item label={t('field.gtin')}>{r.gtin}</Descriptions.Item>
            <Descriptions.Item label={t('field.lot')}>{r.lot}</Descriptions.Item>
            <Descriptions.Item label={t('field.status')}><StatusTag status={r.status} /></Descriptions.Item>
            <Descriptions.Item label={t('lotRecall.classification')}>{r.classification || '–'}</Descriptions.Item>
            <Descriptions.Item label={t('lotRecall.packs')}>{r.scope.packs}</Descriptions.Item>
            <Descriptions.Item label={t('lotRecall.ownStock')}>{r.scope.ownStock}</Descriptions.Item>
            <Descriptions.Item label={t('lotRecall.atHolders')}>{r.scope.atHolders}</Descriptions.Item>
            <Descriptions.Item label={t('lotRecall.noDestination')}>{r.scope.noDestination}</Descriptions.Item>
            <Descriptions.Item label={t('lotRecall.noCustody')}>{r.scope.noCustodyRecord}</Descriptions.Item>
            <Descriptions.Item label={t('field.createdAt')}>{formatDateTime(r.createdAt)}</Descriptions.Item>
            <Descriptions.Item label={t('field.reason')} span={2}>{r.reason}</Descriptions.Item>
          </Descriptions>
          <Space wrap>
            <Button onClick={() => void download('json')}>{t('lotRecall.reportJson')}</Button>
            <Button onClick={() => void download('html')}>{t('lotRecall.reportHtml')}</Button>
            {canWrite && open && (
              <>
                <Button loading={refresh.isPending} onClick={() => refresh.mutate(undefined)}>{t('lotRecall.refresh')}</Button>
                <Button loading={notify.isPending} onClick={() => notify.mutate(undefined)}>{t('lotRecall.notify')}</Button>
                <Popconfirm title={t('lotRecall.close') + '?'} onConfirm={() => close.mutate(undefined)}>
                  <Button danger>{t('lotRecall.close')}</Button>
                </Popconfirm>
              </>
            )}
          </Space>
          <Table<RecallHolderRow>
            size="small"
            rowKey="id"
            pagination={false}
            title={() => t('lotRecall.holders')}
            dataSource={r.holders}
            columns={[
              { title: t('lotRecall.holder'), render: (_v, h) => <Space direction="vertical" size={0}><span>{h.name}</span><Typography.Text type="secondary">{h.gln}</Typography.Text></Space> },
              { title: t('lotRecall.packs'), dataIndex: 'packs' },
              { title: t('field.status'), render: (_v, h) => <Space><StatusTag status={h.status} />{h.partnerOnNetwork && <Tag>{t('lotRecall.onNetwork')}</Tag>}</Space> },
              { title: t('lotRecall.channel'), dataIndex: 'channel', render: (v: string) => v || '–' },
              { title: t('lotRecall.sentAt'), dataIndex: 'sentAt', render: (v?: string) => formatDateTime(v) },
              { title: t('lotRecall.ackAt'), dataIndex: 'acknowledgedAt', render: (v?: string) => formatDateTime(v) },
              {
                title: t('common.actions'),
                render: (_v, h) =>
                  canWrite && open && (
                    <Space>
                      {h.status === 'pending' && <a onClick={() => holder.mutate({ hid: h.id, to: 'sent' })}>{t('lotRecall.markSent')}</a>}
                      {h.status !== 'acknowledged' && <a onClick={() => holder.mutate({ hid: h.id, to: 'acknowledge' })}>{t('lotRecall.acknowledge')}</a>}
                    </Space>
                  ),
              },
            ]}
          />
        </Space>
      )}
    </Drawer>
  );
}

/** Pharma lot recalls: trace a lot forward to its holders and track the notices. */
export default function LotRecalls() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canWrite = hasRole('Compliance');
  const [open, setOpen] = useState<string | null>(null);
  const list = useQuery({ queryKey: KEY, queryFn: () => api.list<LotRecallRow>('/trace/recalls') });
  const create = useSubmit((v: Record<string, unknown>) => api.post<LotRecallRow>('/trace/recalls', v), { invalidate: [KEY], success: t('lotRecall.created') });
  return (
    <PageContainer
      title={t('nav.lotRecalls')}
      subTitle={t('lotRecall.subtitle')}
      extra={canWrite && (
        <ModalForm title={t('lotRecall.create')} trigger={<Button type="primary">{t('lotRecall.create')}</Button>} onFinish={create}>
          <ProFormText name="gtin" label={t('lotRecall.gtin')} rules={[{ required: true }]} />
          <ProFormText name="lot" label={t('lotRecall.lot')} rules={[{ required: true }]} />
          <ProFormText name="title" label={t('lotRecall.title')} />
          <ProFormSelect name="classification" label={t('lotRecall.classification')} options={['I', 'II', 'III']} />
          <ProFormTextArea name="reason" label={t('lotRecall.reason')} rules={[{ required: true }]} />
        </ModalForm>
      )}
    >
      <Table<LotRecallRow>
        size="small"
        rowKey="id"
        loading={list.isLoading}
        dataSource={list.data?.items ?? []}
        columns={[
          { title: t('lotRecall.title'), dataIndex: 'title', render: (v: string, r) => <a onClick={() => setOpen(r.id)}>{v}</a> },
          { title: t('field.gtin'), dataIndex: 'gtin' },
          { title: t('field.lot'), dataIndex: 'lot' },
          { title: t('field.status'), dataIndex: 'status', render: (v: string) => <StatusTag status={v} /> },
          { title: t('field.createdAt'), dataIndex: 'createdAt', render: (v: string) => formatDateTime(v) },
        ]}
      />
      <RecallDrawer id={open} onClose={() => setOpen(null)} />
    </PageContainer>
  );
}
