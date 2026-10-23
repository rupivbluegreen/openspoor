import { useState } from 'react';
import { Alert, Button, Checkbox, Descriptions, Drawer, Input, List, Popconfirm, Space, Tag, Typography } from 'antd';
import { CheckCircleOutlined, DownloadOutlined, ExclamationCircleOutlined, SafetyCertificateOutlined, StopOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, downloadBlob, formatDateTime, notifyError, ProFormDependency, ProFormSelect, ProFormText, StatusTag, useESignature, type InspectionPackage,
} from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { useAction } from '../../lib/hooks';

type Row = InspectionPackage & Record<string, unknown>;
const KEY = ['/agents/inspection/packages'] as const;

function PackageDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { requestSignature, holder } = useESignature();
  const [reason, setReason] = useState('');
  const q = useQuery({ queryKey: [...KEY, id], queryFn: () => api.get<InspectionPackage>(`/agents/inspection/packages/${id}`) });
  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: KEY });
  };
  const confirm = useAction((v: { key: string; confirmed: boolean }) => api.post(`/agents/inspection/packages/${id}/checklist`, v), { invalidate: [KEY], success: false });
  const release = useAction((signatureId: string) => api.post(`/agents/inspection/packages/${id}/release`, { signatureId }), { invalidate: [KEY] });
  const voidIt = useAction(() => api.post(`/agents/inspection/packages/${id}/void`, { reason }), { invalidate: [KEY] });
  const p = q.data;
  const draft = p?.status === 'draft';
  const open = p?.checklist.filter((c) => c.status !== 'ok' && !c.confirmedBy).length ?? 0;
  const download = async () => {
    try {
      const body = await api.get<string>(`/agents/inspection/packages/${id}/content`, undefined, { responseType: 'text' });
      const csv = p?.kind === 'fsma204';
      downloadBlob(body, `${p?.kind}-${p?.status}-${id.slice(0, 8)}.${csv ? 'csv' : 'json'}`, csv ? 'text/csv;charset=utf-8' : 'application/json');
    } catch (e) {
      notifyError(e);
    }
  };
  return (
    <Drawer open width={640} onClose={onClose} title={p?.title ?? '…'} loading={q.isLoading} extra={p && <StatusTag status={p.status} />}>
      {p && (
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <Alert type="info" showIcon message={t('agents.inspection.nothingSent')} />
          <Descriptions size="small" column={1} bordered>
            <Descriptions.Item label={t('field.createdAt')}>{formatDateTime(p.createdAt)} · {p.createdBy}</Descriptions.Item>
            <Descriptions.Item label={t('agents.inspection.sha256')}><Typography.Text code copyable>{p.sha256}</Typography.Text></Descriptions.Item>
            {typeof p.summary?.rows === 'number' && <Descriptions.Item label={t('agents.inspection.rows')}>{p.summary.rows}</Descriptions.Item>}
            {typeof p.summary?.items === 'number' && <Descriptions.Item label={t('agents.identifiers')}>{p.summary.items}</Descriptions.Item>}
            {p.releasedAt && <Descriptions.Item label={t('agents.inspection.releasedBy')}>{p.releasedBy} · {formatDateTime(p.releasedAt)}</Descriptions.Item>}
          </Descriptions>
          <List
            header={<strong>{t('agents.inspection.checklist')}</strong>}
            bordered
            dataSource={p.checklist}
            renderItem={(c) => {
              const done = c.status === 'ok' || !!c.confirmedBy;
              return (
                <List.Item>
                  <Space align="start">
                    {c.kind === 'auto' && c.status === 'ok' ? (
                      <CheckCircleOutlined style={{ color: 'var(--ant-color-success)', marginTop: 4 }} />
                    ) : (
                      <Checkbox checked={done} disabled={!draft || confirm.isPending} onChange={(e) => confirm.mutate({ key: c.key, confirmed: e.target.checked })} />
                    )}
                    <div>
                      <div>{c.title} {c.status === 'warn' && !done && <ExclamationCircleOutlined style={{ color: 'var(--ant-color-warning)' }} />}</div>
                      {c.detail && <Typography.Text type="secondary">{c.detail}</Typography.Text>}
                      <div>
                        <Tag>{c.kind === 'auto' ? t('agents.inspection.agentChecked') : t('agents.inspection.confirm')}</Tag>
                        {c.confirmedBy && <Typography.Text type="secondary">{t('agents.inspection.confirmedBy')} {c.confirmedBy}</Typography.Text>}
                      </div>
                    </div>
                  </Space>
                </List.Item>
              );
            }}
          />
          <Space wrap>
            <Button icon={<DownloadOutlined />} onClick={download}>{t('agents.inspection.download')}</Button>
            {draft && (
              <Button
                type="primary"
                icon={<SafetyCertificateOutlined />}
                disabled={open > 0}
                loading={release.isPending}
                onClick={async () => {
                  const sig = await requestSignature({ entity: 'inspection_package', entityId: id, defaultMeaning: 'approval' });
                  if (sig) release.mutate(sig.id, { onSuccess: refresh });
                }}
              >
                {t('agents.inspection.release')}
              </Button>
            )}
          </Space>
          {draft && <Typography.Text type="secondary">{t('agents.inspection.releaseHint')}</Typography.Text>}
          {p.status !== 'voided' && (
            <Space.Compact style={{ width: '100%' }}>
              <Input placeholder={t('agents.inspection.voidReason')} value={reason} onChange={(e) => setReason(e.target.value)} />
              <Popconfirm title={`${t('agents.inspection.void')}?`} onConfirm={() => voidIt.mutateAsync(undefined)} disabled={!reason.trim()}>
                <Button danger icon={<StopOutlined />} disabled={!reason.trim()} loading={voidIt.isPending}>{t('agents.inspection.void')}</Button>
              </Popconfirm>
            </Space.Compact>
          )}
          {holder}
        </Space>
      )}
    </Drawer>
  );
}

/** Draft packages assembled by the inspection agent; a person confirms the checklist and signs to release. */
export default function InspectionPackages() {
  const { t } = useTranslation();
  const [openId, setOpenId] = useState<string | null>(null);
  const kinds = [
    { value: 'fsma204', label: t('agents.inspection.fsma204') },
    { value: 'tracing', label: t('agents.inspection.tracing') },
  ];
  return (
    <>
      <CrudPage<Row>
        title={t('nav.inspectionPackages')}
        subTitle={t('agents.inspection.subtitle')}
        path="/agents/inspection/packages"
        columns={[
          { title: t('field.title'), dataIndex: 'title', render: (_d, r) => <a onClick={() => setOpenId(r.id)}>{r.title}</a> },
          { title: t('agents.inspection.kind'), dataIndex: 'kind', render: (_d, r) => kinds.find((k) => k.value === r.kind)?.label ?? r.kind },
          { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
          {
            title: t('agents.inspection.checklist'),
            render: (_d, r) => `${r.checklist.filter((c) => c.status === 'ok' || c.confirmedBy).length}/${r.checklist.length}`,
          },
          { title: t('field.createdAt'), dataIndex: 'createdAt', render: (_d, r) => formatDateTime(r.createdAt) },
        ]}
        create={{
          label: t('agents.inspection.assemble'),
          initialValues: { kind: 'fsma204' },
          fields: (
            <>
              <ProFormSelect name="kind" label={t('agents.inspection.kind')} options={kinds} rules={[{ required: true }]} allowClear={false} />
              <ProFormDependency name={['kind']}>
                {({ kind }) =>
                  kind === 'tracing' ? (
                    <ProFormText name="investigationId" label={t('agents.inspection.investigationId')} rules={[{ required: true }]} />
                  ) : (
                    <>
                      <ProFormText name="from" label={t('agents.inspection.from')} placeholder="YYYY-MM-DD" />
                      <ProFormText name="to" label={t('agents.inspection.to')} placeholder="YYYY-MM-DD" />
                      <ProFormText name="tlc" label={t('agents.inspection.tlc')} />
                    </>
                  )
                }
              </ProFormDependency>
            </>
          ),
          transform: (v) => {
            const scope = v.kind === 'tracing' ? { investigationId: v.investigationId } : { from: v.from || undefined, to: v.to || undefined, tlc: v.tlc || undefined };
            return { kind: v.kind, scope };
          },
        }}
        rowActions={(r) => [
          <a key="open" onClick={() => setOpenId(r.id)}>
            {t('agents.inspection.open')}
          </a>,
        ]}
      />
      {openId && <PackageDrawer id={openId} onClose={() => setOpenId(null)} />}
    </>
  );
}
