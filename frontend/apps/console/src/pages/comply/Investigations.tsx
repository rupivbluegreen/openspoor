import { useState } from 'react';
import { Alert, Button, Card, Drawer, Empty, Flex, List, Modal, Popconfirm, Segmented, Select, Space, Switch, Table, Tag, Timeline, Typography } from 'antd';
import { CloudDownloadOutlined, FileSearchOutlined, LockOutlined, PlusOutlined, SettingOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api,
  CodeView,
  downloadBlob,
  formatDateTime,
  ModalForm,
  PageContainer,
  prettyJson,
  ProDescriptions,
  ProFormDateTimePicker,
  ProFormDigit,
  ProFormSelect,
  ProFormText,
  ProFormTextArea,
  QueryTable,
  StatusTag,
  useESignature,
  type Investigation,
  type InvestigationDetail,
  type InvestigationPolicy,
  type InvestigationStatus,
  type Partner,
} from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';
import { useSession } from '../../lib/session';

type Row = Investigation & Record<string, unknown>;

const KINDS = [
  { value: 'tracing_request', label: 'Tracing request' },
  { value: 'suspect', label: 'Suspect product' },
  { value: 'illegitimate', label: 'Illegitimate product' },
];
const kindLabel = (k: string) => KINDS.find((x) => x.value === k)?.label ?? k;

const STATUS_LABEL: Record<InvestigationStatus, string> = {
  open: 'Open',
  quarantined: 'Quarantine',
  investigating: 'Start investigation',
  cleared: 'Clear',
  confirmed_illegitimate: 'Confirm illegitimate',
  closed: 'Close',
};

const FINDING_KINDS = [
  { value: 'note', label: 'Note' },
  { value: 'evidence', label: 'Evidence' },
  { value: 'verification', label: 'Verification' },
  { value: 'partner_notification', label: 'Trading partner notified' },
  { value: 'authority_notification', label: 'Authority notified' },
  { value: 'consultation', label: 'Consultation' },
  { value: 'notification_terminated', label: 'Notification terminated' },
];

const lines = (v?: string) => (v ?? '').split(/[\s,;]+/).map((s) => s.trim()).filter(Boolean);
const needsSignature = (i: Investigation) => i.status === 'confirmed_illegitimate' || i.kind === 'illegitimate';

function timelineColor(kind: string, to: string) {
  if (kind === 'finding') return 'gray';
  if (to === 'confirmed_illegitimate') return 'red';
  if (to === 'cleared' || to === 'closed') return 'green';
  if (to === 'quarantined') return 'orange';
  return 'blue';
}

function InvestigationDrawer({ id, onClose, canWrite }: { id: string | null; onClose: () => void; canWrite: boolean }) {
  const { t } = useTranslation();
  const key = ['/investigations', id] as const;
  const q = useQuery({ queryKey: key, queryFn: () => api.get<InvestigationDetail>(`/investigations/${id}`), enabled: !!id });
  const inv = q.data?.investigation;
  const inval = [['/investigations']] as const;
  const { requestSignature, holder } = useESignature();
  const [closing, setClosing] = useState(false);

  const transition = useAction((to: InvestigationStatus) => api.post(`/investigations/${id}/transition`, { to }), { invalidate: inval });
  const addFinding = useSubmit((v: { kind: string; summary: string; reference?: string }) =>
    api.post(`/investigations/${id}/findings`, { kind: v.kind, summary: v.summary, detail: v.reference ? { reference: v.reference } : undefined }), { invalidate: inval });
  const assemble = useAction(() => api.post<unknown>(`/investigations/${id}/tracing-answer`), {
    invalidate: inval,
    onSuccess: (ans) => downloadBlob(prettyJson(ans), `tracing-answer-${id}.json`, 'application/json'),
  });
  const download = async () => {
    const ans = await api.get<unknown>(`/investigations/${id}/tracing-answer`);
    downloadBlob(prettyJson(ans), `tracing-answer-${id}.json`, 'application/json');
  };
  const close = useSubmit(
    async (v: { decision: string; note?: string }) => {
      let signatureId: string | undefined;
      if (inv && needsSignature(inv)) {
        const sig = await requestSignature({ entity: 'investigation', entityId: inv.id, defaultMeaning: 'approval', defaultReason: v.decision });
        if (!sig) throw new Error('signature cancelled');
        signatureId = sig.id;
      }
      return api.post(`/investigations/${id}/close`, { ...v, signatureId });
    },
    { invalidate: inval, onSuccess: () => setClosing(false) },
  );

  const next = (q.data?.allowedTransitions ?? []).filter((s) => s !== 'closed');
  const canClose = q.data?.allowedTransitions.includes('closed');

  return (
    <Drawer open={!!id} onClose={onClose} title={inv?.title ?? id} size="large" loading={q.isLoading} extra={inv && <StatusTag status={inv.status} />}>
      {inv ? (
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          {inv.overdue && <Alert type="error" showIcon title={`Overdue since ${formatDateTime(inv.dueAt)}`} />}
          {canWrite && (next.length > 0 || canClose) && (
            <Flex gap={8} wrap>
              {next.map((s) => (
                <Popconfirm key={s} title={`${STATUS_LABEL[s]}?`} description={t('common.audited')} onConfirm={() => transition.mutateAsync(s)}>
                  <Button type={s === 'confirmed_illegitimate' ? 'primary' : 'default'} danger={s === 'confirmed_illegitimate'} loading={transition.isPending}>
                    {STATUS_LABEL[s]}
                  </Button>
                </Popconfirm>
              ))}
              {canClose && (
                <Button type="primary" icon={needsSignature(inv) ? <LockOutlined /> : undefined} onClick={() => setClosing(true)}>
                  {STATUS_LABEL.closed}
                </Button>
              )}
            </Flex>
          )}
          <ProDescriptions<Investigation>
            column={1}
            size="small"
            dataSource={inv}
            columns={[
              { title: t('field.kind'), dataIndex: 'kind', render: (_d, x) => <Tag>{kindLabel(x.kind)}</Tag> },
              { title: t('field.gtin'), dataIndex: 'gtin', render: (_d, x) => x.gtin || '–' },
              { title: 'Serials', dataIndex: 'serials', render: (_d, x) => (x.serials.length ? x.serials.map((s) => <Tag key={s}>{s}</Tag>) : '–') },
              { title: 'Lots', dataIndex: 'lots', render: (_d, x) => (x.lots.length ? x.lots.map((s) => <Tag key={s}>{s}</Tag>) : '–') },
              { title: 'Identifiers in scope', dataIndex: 'epcs', render: (_d, x) => `${x.epcs.length}${x.notFound.length ? ` (${x.notFound.length} not found)` : ''}${x.held ? ' · held' : ''}` },
              { title: 'Requester', dataIndex: 'requesterName', render: (_d, x) => (x.requesterType ? `${x.requesterName} (${x.requesterType})${x.requestRef ? ` · ${x.requestRef}` : ''}` : '–') },
              { title: 'Due', dataIndex: 'dueAt', render: (_d, x) => (x.dueAt ? formatDateTime(x.dueAt) : '–') },
              { title: 'Decision', dataIndex: 'decision', render: (_d, x) => x.decision || '–' },
              { title: t('esig.title'), dataIndex: 'signatureId', render: (_d, x) => x.signatureId ?? '–' },
            ]}
          />
          <Card
            size="small"
            title="Tracing answer"
            extra={
              <Space>
                {inv.answeredAt && (
                  <Button size="small" icon={<CloudDownloadOutlined />} onClick={() => void download()}>
                    {t('action.download')}
                  </Button>
                )}
                {canWrite && inv.status !== 'closed' && (
                  <Button size="small" type="primary" icon={<FileSearchOutlined />} loading={assemble.isPending} onClick={() => assemble.mutate(undefined)}>
                    Assemble
                  </Button>
                )}
              </Space>
            }
          >
            <Typography.Text type="secondary">
              {inv.answeredAt
                ? `Assembled ${formatDateTime(inv.answeredAt)}. Review it before sending it to the requester.`
                : 'Builds the transaction history (TI/TS per shipment and the event history of every identifier in scope) for review.'}
            </Typography.Text>
          </Card>
          <Card
            size="small"
            title={`Findings (${inv.findings.length})`}
            extra={
              canWrite && inv.status !== 'closed' ? (
                <ModalForm<{ kind: string; summary: string; reference?: string }>
                  title="Add finding"
                  width={520}
                  trigger={<Button size="small" icon={<PlusOutlined />}>{t('action.create')}</Button>}
                  modalProps={{ destroyOnHidden: true }}
                  initialValues={{ kind: 'note' }}
                  onFinish={addFinding}
                >
                  <ProFormSelect name="kind" label={t('field.kind')} options={FINDING_KINDS} rules={[{ required: true }]} />
                  <ProFormTextArea name="summary" label={t('field.description')} rules={[{ required: true }]} fieldProps={{ rows: 3 }} />
                  <ProFormText name="reference" label="Reference" />
                </ModalForm>
              ) : null
            }
          >
            <List
              size="small"
              dataSource={inv.findings}
              locale={{ emptyText: t('common.empty') }}
              renderItem={(f) => (
                <List.Item>
                  <List.Item.Meta title={<Space><Tag>{FINDING_KINDS.find((k) => k.value === f.kind)?.label ?? f.kind}</Tag>{f.summary}</Space>} description={`${f.by} · ${formatDateTime(f.at)}`} />
                </List.Item>
              )}
            />
          </Card>
          <Card size="small" title="Timeline">
            <Timeline
              items={(q.data?.timeline ?? []).map((ev) => ({
                key: ev.id,
                color: timelineColor(ev.kind, ev.toStatus),
                children: (
                  <>
                    <Typography.Text strong>{ev.kind === 'transition' ? `${ev.fromStatus} → ${ev.toStatus}` : ev.kind.replace(/_/g, ' ')}</Typography.Text>
                    <div>
                      <Typography.Text type="secondary">{`${ev.actor} · ${formatDateTime(ev.createdAt)}`}</Typography.Text>
                    </div>
                    {ev.note && <div>{ev.note}</div>}
                    {Object.keys(ev.data ?? {}).length > 0 && (
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {Object.entries(ev.data).filter(([, v]) => v !== null && v !== undefined).map(([k, v]) => `${k}: ${typeof v === 'object' ? JSON.stringify(v) : String(v)}`).join(' · ')}
                      </Typography.Text>
                    )}
                  </>
                ),
              }))}
            />
          </Card>
          {inv.notFound.length > 0 && (
            <details>
              <summary>Not found in the repository ({inv.notFound.length})</summary>
              <CodeView code={inv.notFound.join('\n')} maxHeight={200} />
            </details>
          )}
          <ModalForm<{ decision: string; note?: string }>
            title={needsSignature(inv) ? 'Close with electronic signature' : 'Close investigation'}
            open={closing}
            onOpenChange={setClosing}
            width={520}
            modalProps={{ destroyOnHidden: true }}
            onFinish={close}
          >
            {needsSignature(inv) && <Alert type="warning" showIcon style={{ marginBottom: 16 }} title="Closing an illegitimate product investigation requires your electronic signature." />}
            <ProFormTextArea name="decision" label="Decision" rules={[{ required: true }]} fieldProps={{ rows: 3 }} />
            <ProFormTextArea name="note" label="Note" fieldProps={{ rows: 2 }} />
          </ModalForm>
          {holder}
        </Space>
      ) : (
        <Empty />
      )}
    </Drawer>
  );
}

function PoliciesModal({ open, onClose, canWrite }: { open: boolean; onClose: () => void; canWrite: boolean }) {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ['/investigations/policies'], queryFn: () => api.list<InvestigationPolicy>('/investigations/policies'), enabled: open });
  const save = useSubmit((v: InvestigationPolicy) => api.put(`/investigations/policies/${v.kind}`, { dueHours: v.dueHours, basis: v.basis }), {
    invalidate: [['/investigations/policies']],
  });
  return (
    <Modal open={open} onCancel={onClose} footer={null} width={760} title="Response windows">
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        title="Defaults are placeholders, not verified deadlines. Set each window from your regulatory procedure."
      />
      <Table<InvestigationPolicy>
        size="small"
        rowKey="kind"
        pagination={false}
        loading={q.isLoading}
        dataSource={q.data?.items ?? []}
        columns={[
          { title: t('field.kind'), dataIndex: 'kind', render: (k: string) => kindLabel(k) },
          { title: 'Hours', dataIndex: 'dueHours', render: (h: number) => (h ? h : 'No due date') },
          { title: 'Basis', dataIndex: 'basis', render: (b: string, r) => <>{r.isDefault && <Tag color="orange">default</Tag>}{b}</> },
          {
            title: t('common.actions'),
            render: (_d, r) =>
              canWrite ? (
                <ModalForm<InvestigationPolicy>
                  title={kindLabel(r.kind)}
                  width={480}
                  trigger={<a>{t('action.edit')}</a>}
                  modalProps={{ destroyOnHidden: true }}
                  initialValues={{ ...r, basis: r.isDefault ? '' : r.basis }}
                  onFinish={(v) => save({ ...v, kind: r.kind })}
                >
                  <ProFormDigit name="dueHours" label="Hours (0 = no due date)" min={0} max={8760} rules={[{ required: true }]} />
                  <ProFormTextArea name="basis" label="Basis" rules={[{ required: true }]} fieldProps={{ rows: 2 }} />
                </ModalForm>
              ) : null,
          },
        ]}
      />
    </Modal>
  );
}

interface CreateValues {
  kind: string;
  title?: string;
  gtin?: string;
  serials?: string;
  lots?: string;
  epcs?: string;
  requesterType?: 'partner' | 'authority';
  partnerId?: string;
  requesterName?: string;
  requestRef?: string;
  dueAt?: string;
  note?: string;
}

export default function Investigations() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canWrite = hasRole('Compliance');
  const [status, setStatus] = useState('active');
  const [kind, setKind] = useState<string | undefined>();
  const [overdue, setOverdue] = useState(false);
  const [openId, setOpenId] = useState<string | null>(null);
  const [policies, setPolicies] = useState(false);
  const partners = useQuery({ queryKey: ['/partners'], queryFn: () => api.list<Partner>('/partners'), enabled: canWrite });

  const create = useSubmit(
    (v: CreateValues) =>
      api.post<Investigation>('/investigations', {
        kind: v.kind,
        title: v.title,
        gtin: v.gtin,
        serials: lines(v.serials),
        lots: lines(v.lots),
        epcs: lines(v.epcs),
        requester: v.requesterType ? { type: v.requesterType, partnerId: v.partnerId, name: v.requesterName } : undefined,
        requestRef: v.requestRef,
        dueAt: v.dueAt ? new Date(v.dueAt).toISOString() : undefined,
        note: v.note,
      }),
    { invalidate: [['/investigations']], onSuccess: (inv) => setOpenId(inv.id) },
  );

  const params = { status: status === 'all' ? undefined : status, kind, overdue: overdue ? 'true' : undefined };

  return (
    <PageContainer
      title={t('nav.investigations')}
      subTitle={t('common.audited')}
      extra={
        <Space wrap>
          <Select allowClear placeholder={t('field.kind')} style={{ minWidth: 180 }} value={kind} onChange={setKind} options={KINDS} />
          <Space size={4}>
            <Switch size="small" checked={overdue} onChange={setOverdue} />
            <Typography.Text>Overdue</Typography.Text>
          </Space>
          <Segmented
            value={status}
            onChange={(v) => setStatus(String(v))}
            options={[
              { value: 'active', label: t('common.open') },
              { value: 'closed', label: 'Closed' },
              { value: 'all', label: t('common.all') },
            ]}
          />
        </Space>
      }
    >
      <QueryTable<Row>
        rowKey="id"
        tableId="investigations"
        queryKey={['/investigations']}
        extraParams={params}
        fetcher={() => api.list<Row>('/investigations', params)}
        columns={[
          { title: t('field.title'), dataIndex: 'title', render: (_d, r) => <a onClick={() => setOpenId(r.id)}>{r.title || r.id}</a> },
          { title: t('field.kind'), dataIndex: 'kind', render: (_d, r) => <Tag>{kindLabel(r.kind)}</Tag>, exportValue: (r) => r.kind },
          { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
          { title: t('field.gtin'), dataIndex: 'gtin' },
          { title: 'Identifiers', dataIndex: 'epcs', render: (_d, r) => r.epcs.length, exportValue: (r) => r.epcs.length },
          { title: 'Requester', dataIndex: 'requesterName' },
          {
            title: 'Due',
            dataIndex: 'dueAt',
            render: (_d, r) => (r.dueAt ? <Space size={4}>{formatDateTime(r.dueAt)}{r.overdue && <Tag color="red">overdue</Tag>}</Space> : '–'),
          },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
        ]}
        toolBarRender={() => [
          <Button key="pol" icon={<SettingOutlined />} onClick={() => setPolicies(true)}>
            Response windows
          </Button>,
          canWrite ? (
            <ModalForm<CreateValues>
              key="new"
              title="Open investigation"
              width={640}
              trigger={<Button type="primary" icon={<PlusOutlined />}>{t('action.create')}</Button>}
              modalProps={{ destroyOnHidden: true }}
              initialValues={{ kind: 'suspect' }}
              onFinish={create}
            >
              <ProFormSelect name="kind" label={t('field.kind')} options={KINDS} rules={[{ required: true }]} />
              <ProFormText name="title" label={t('field.title')} />
              <ProFormText name="gtin" label={t('field.gtin')} />
              <ProFormTextArea name="serials" label="Serials (one per line)" fieldProps={{ rows: 2 }} />
              <ProFormTextArea name="lots" label="Lots (one per line)" fieldProps={{ rows: 1 }} />
              <ProFormTextArea name="epcs" label="Other identifiers (EPC URN or Digital Link, e.g. SSCC)" fieldProps={{ rows: 2 }} />
              <ProFormSelect
                name="requesterType"
                label="Requester"
                options={[
                  { value: 'partner', label: 'Trading partner' },
                  { value: 'authority', label: 'Authority' },
                ]}
              />
              <ProFormSelect name="partnerId" label={t('field.partner')} options={(partners.data?.items ?? []).map((p) => ({ value: p.id, label: p.name }))} />
              <ProFormText name="requesterName" label="Requester name" />
              <ProFormText name="requestRef" label="Request reference" />
              <ProFormDateTimePicker name="dueAt" label="Due (leave empty for the configured window)" />
              <ProFormTextArea name="note" label="Note" fieldProps={{ rows: 2 }} />
            </ModalForm>
          ) : null,
        ]}
      />
      <InvestigationDrawer id={openId} onClose={() => setOpenId(null)} canWrite={canWrite} />
      <PoliciesModal open={policies} onClose={() => setPolicies(false)} canWrite={canWrite} />
    </PageContainer>
  );
}
