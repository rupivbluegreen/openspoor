import { useState } from 'react';
import { Alert, Button, Card, Drawer, Dropdown, Empty, Flex, InputNumber, List, Select, Space, Switch, Tag, Typography } from 'antd';
import { DownloadOutlined, HistoryOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  api, downloadBlob, formatDateTime, notifyError, notifySuccess, overviewShapes, PageContainer, ProFormDependency, ProFormList, ProFormSelect,
  ProFormSwitch, ProFormText, ProFormTextArea, StepsForm, type Plot, type TraceabilityPlanContent, type TraceabilityPlanStatus, type TraceabilityPlanVersion,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import { useProductOptions, useSiteOptions, usePlotOptions } from '../../lib/options';
import { useSession } from '../../lib/session';

const BASE = '/food/traceability-plan';
const SECTIONS = ['recordProcedures', 'foodsHandled', 'tlcAssignment', 'pointOfContact', 'farmMap'] as const;

const EMPTY: TraceabilityPlanContent = {
  recordProcedures: { text: '' },
  foodsHandled: { text: '', items: [] },
  tlcAssignment: { text: '' },
  pointOfContact: { text: '' },
  farmMap: { applicable: false, text: '', plotIds: [] },
};

/** Schematic overview of registered plots; GeoJSON is projected locally, no base map. */
function PlotOverview({ plotIds }: { plotIds: string[] }) {
  const { t } = useTranslation();
  const plots = useQuery({ queryKey: ['/food/plots'], queryFn: () => api.list<Plot & Record<string, unknown>>('/food/plots'), meta: { silent: true } });
  const chosen = (plots.data?.items ?? []).filter((p) => plotIds.length === 0 || (p.id && plotIds.includes(p.id)));
  const shapes = overviewShapes(chosen);
  return (
    <Card size="small" title={t('traceabilityPlan.plotOverview')} extra={<Link to="/food/plots">{t('traceabilityPlan.managePlots')}</Link>} style={{ marginBottom: 16 }}>
      {shapes.length === 0 ? (
        <Empty description={t('traceabilityPlan.noPlots')} />
      ) : (
        <svg viewBox="0 0 720 420" role="img" aria-label={t('traceabilityPlan.plotOverview')} style={{ width: '100%', height: 'auto', border: '1px solid var(--ant-color-border, #ddd)' }}>
          {shapes.map((s, i) =>
            s.isPoint ? (
              <g key={i}>
                <circle cx={s.points[0][0]} cy={s.points[0][1]} r={5} fill="#2a7" />
                <text x={s.points[0][0] + 8} y={s.points[0][1] + 4} fontSize={11} fill="currentColor">{s.name}</text>
              </g>
            ) : (
              <g key={i}>
                <polygon points={s.points.map((p) => p.join(',')).join(' ')} fill="#2a7" fillOpacity={0.35} stroke="#165" />
                <text x={s.points[0][0]} y={s.points[0][1] - 3} fontSize={11} fill="currentColor">{s.name}</text>
              </g>
            ),
          )}
        </svg>
      )}
      <Typography.Text type="secondary">{t('traceabilityPlan.overviewNote')}</Typography.Text>
    </Card>
  );
}

function ReviewSettings({ status, canWrite }: { status: TraceabilityPlanStatus; canWrite: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [on, setOn] = useState(status.settings.reviewReminder);
  const [months, setMonths] = useState(status.settings.reviewMonths);
  return (
    <Card size="small" title={t('traceabilityPlan.settings')} style={{ marginBottom: 16 }}>
      <Flex gap={12} align="center" wrap>
        <Switch checked={on} disabled={!canWrite} onChange={setOn} aria-label={t('traceabilityPlan.reviewReminder')} />
        <span>{t('traceabilityPlan.reviewReminder')}</span>
        <span>{t('traceabilityPlan.reviewMonths')}</span>
        <InputNumber min={1} max={60} precision={0} value={months} disabled={!canWrite || !on} onChange={(v) => setMonths(v ?? 12)} />
        {canWrite && (
          <Button
            onClick={() => {
              api.put(`${BASE}/settings`, { reviewReminder: on, reviewMonths: months }).then(
                () => {
                  notifySuccess(t('traceabilityPlan.settingsSaved'));
                  void qc.invalidateQueries({ queryKey: [BASE] });
                },
                notifyError,
              );
            }}
          >
            {t('common.save')}
          </Button>
        )}
      </Flex>
      <Typography.Text type="secondary">{t('traceabilityPlan.reviewHelp')}</Typography.Text>
    </Card>
  );
}

export default function TraceabilityPlan() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canWrite = hasRole('Admin', 'Ops', 'Compliance');
  const [siteId, setSiteId] = useState<string | undefined>();
  const [historyOpen, setHistoryOpen] = useState(false);
  const [seed, setSeed] = useState<TraceabilityPlanVersion | null>(null);
  const sites = useSiteOptions();
  const products = useProductOptions();
  const plotOptions = usePlotOptions();
  const q = useQuery({ queryKey: [BASE, siteId ?? ''], queryFn: () => api.get<TraceabilityPlanStatus>(BASE, { siteId }) });
  const versions = useQuery({ queryKey: [BASE, 'versions', siteId ?? ''], queryFn: () => api.list<TraceabilityPlanVersion & Record<string, unknown>>(`${BASE}/versions`, { siteId }), enabled: historyOpen });
  const status = q.data;
  const plan = status?.plan ?? null;
  const base = seed ?? plan;
  const initial: TraceabilityPlanContent = { ...EMPTY, ...(base?.content ?? {}) };

  const save = useSubmit(
    (v: { content: TraceabilityPlanContent; reason: string }) => api.put<TraceabilityPlanVersion>(BASE, { siteId, reason: v.reason, content: v.content }),
    { invalidate: [[BASE]], success: false, onSuccess: (r) => { setSeed(null); notifySuccess(t('traceabilityPlan.saved', { n: r.version })); } },
  );

  const exportAs = async (format: 'json' | 'html' | 'md', version?: number) => {
    try {
      const body = await api.get<string>(`${BASE}/export`, { format, siteId, version }, { responseType: 'text' });
      const types = { json: 'application/json', html: 'text/html;charset=utf-8', md: 'text/markdown;charset=utf-8' };
      downloadBlob(body, `traceability-plan-v${version ?? plan?.version ?? 0}.${format}`, types[format]);
    } catch (e) {
      notifyError(e);
    }
  };
  const exportMenu = (version?: number) => ({
    items: [
      { key: 'json', label: t('traceabilityPlan.exportJson') },
      { key: 'html', label: t('traceabilityPlan.exportHtml') },
      { key: 'md', label: t('traceabilityPlan.exportMarkdown') },
    ],
    onClick: ({ key }: { key: string }) => void exportAs(key as 'json' | 'html' | 'md', version),
  });

  const secTitle = (n: number) => t(`traceabilityPlan.s${n}`);
  const help = (n: number) => (
    <Typography.Paragraph type="secondary">
      21 CFR 1.1315{t(`traceabilityPlan.s${n}Ref`)} · {t(`traceabilityPlan.s${n}Help`)}
    </Typography.Paragraph>
  );
  const disabled = !canWrite;

  return (
    <PageContainer
      title={t('nav.traceabilityPlan')}
      subTitle={t('traceabilityPlan.subtitle')}
      extra={[
        <Select
          key="site"
          allowClear
          style={{ minWidth: 220 }}
          placeholder={t('traceabilityPlan.siteAll')}
          aria-label={t('traceabilityPlan.siteLabel')}
          options={sites}
          value={siteId}
          onChange={(v) => { setSiteId(v); setSeed(null); }}
        />,
        <Button key="hist" icon={<HistoryOutlined />} onClick={() => setHistoryOpen(true)}>{t('traceabilityPlan.history')}</Button>,
        <Dropdown key="exp" menu={exportMenu()} disabled={!plan}>
          <Button icon={<DownloadOutlined />} disabled={!plan}>{t('traceabilityPlan.export')}</Button>
        </Dropdown>,
      ]}
    >
      <Alert type="info" showIcon title={t('traceabilityPlan.ruleNote')} style={{ marginBottom: 16 }} />
      {!canWrite && <Alert type="warning" showIcon title={t('traceabilityPlan.readOnly')} style={{ marginBottom: 16 }} />}
      {status?.review.due && plan && <Alert type="warning" showIcon title={t('traceabilityPlan.reviewDue', { date: formatDateTime(plan.createdAt) })} style={{ marginBottom: 16 }} />}
      {status && !plan && <Alert type="info" showIcon title={t('traceabilityPlan.noPlan')} style={{ marginBottom: 16 }} />}
      {status && status.completenessHints.length > 0 && plan && (
        <Alert type="info" showIcon title={t('traceabilityPlan.incomplete', { list: status.completenessHints.map((h) => secTitle(SECTIONS.indexOf(h as (typeof SECTIONS)[number]) + 1)).join(', ') })} style={{ marginBottom: 16 }} />
      )}
      {status && <ReviewSettings key={`${status.settings.reviewReminder}-${status.settings.reviewMonths}`} status={status} canWrite={hasRole('Admin', 'Ops', 'Compliance')} />}

      {status && (
        <Card key={`${base?.id ?? 'new'}-${siteId ?? ''}`}>
          <StepsForm<{ content: TraceabilityPlanContent; reason: string }>
            onFinish={async (v) => {
              const merged = v as unknown as TraceabilityPlanContent & { reason: string };
              const content: TraceabilityPlanContent = {
                recordProcedures: merged.recordProcedures ?? initial.recordProcedures,
                foodsHandled: { text: merged.foodsHandled?.text ?? '', items: (merged.foodsHandled?.items ?? []).map((i) => ({ ...i, productId: i.productId || undefined })) },
                tlcAssignment: merged.tlcAssignment ?? initial.tlcAssignment,
                pointOfContact: merged.pointOfContact ?? initial.pointOfContact,
                farmMap: { applicable: !!merged.farmMap?.applicable, text: merged.farmMap?.text ?? '', plotIds: merged.farmMap?.plotIds ?? [] },
              };
              return save({ content, reason: merged.reason });
            }}
            submitter={disabled ? { render: ({ step, onPre, onSubmit }) => [step > 0 && <Button key="p" onClick={onPre}>‹</Button>, step < 5 && <Button key="n" type="primary" onClick={onSubmit}>›</Button>] } : undefined}
            formProps={{ disabled: false }}
          >
            <StepsForm.StepForm name="s1" title={secTitle(1)} initialValues={{ recordProcedures: initial.recordProcedures }}>
              {help(1)}
              <ProFormTextArea name={['recordProcedures', 'text']} label={t('traceabilityPlan.description')} fieldProps={{ rows: 8, maxLength: 20000, disabled }} />
            </StepsForm.StepForm>

            <StepsForm.StepForm name="s2" title={secTitle(2)} initialValues={{ foodsHandled: initial.foodsHandled }}>
              {help(2)}
              <ProFormTextArea name={['foodsHandled', 'text']} label={t('traceabilityPlan.description')} fieldProps={{ rows: 5, maxLength: 20000, disabled }} />
              <datalist id="ftl-categories">{(status.context.ftlCategories ?? []).map((c) => <option key={c} value={c} />)}</datalist>
              <ProFormList
                name={['foodsHandled', 'items']}
                label={t('traceabilityPlan.foods')}
                creatorButtonProps={disabled ? false : { creatorButtonText: t('traceabilityPlan.addFood') }}
                copyIconProps={false}
                deleteIconProps={disabled ? false : undefined}
              >
                <Space wrap align="start">
                  <ProFormSelect name="productId" label={t('traceabilityPlan.product')} width="md" options={products} allowClear fieldProps={{ showSearch: true, optionFilterProp: 'label', disabled }} />
                  <ProFormText name="name" label={t('traceabilityPlan.foodName')} width="sm" fieldProps={{ maxLength: 300, disabled }} />
                  <ProFormText name="ftlCategory" label={t('traceabilityPlan.ftlCategory')} width="md" tooltip={t('traceabilityPlan.ftlHelp')} fieldProps={{ list: 'ftl-categories', maxLength: 200, disabled }} rules={[{ required: true }]} />
                  <ProFormText name="note" label={t('traceabilityPlan.note')} width="sm" fieldProps={{ maxLength: 1000, disabled }} />
                </Space>
              </ProFormList>
            </StepsForm.StepForm>

            <StepsForm.StepForm name="s3" title={secTitle(3)} initialValues={{ tlcAssignment: initial.tlcAssignment }}>
              {help(3)}
              <ProFormTextArea name={['tlcAssignment', 'text']} label={t('traceabilityPlan.description')} fieldProps={{ rows: 6, maxLength: 20000, disabled }} />
              <Space wrap align="start">
                <ProFormText name={['tlcAssignment', 'pattern']} label={t('traceabilityPlan.tlcPattern')} width="md" fieldProps={{ maxLength: 500, disabled }} placeholder="SP-YYYYMMDD-NNN" />
                <ProFormText name={['tlcAssignment', 'source']} label={t('traceabilityPlan.tlcSource')} width="md" fieldProps={{ list: 'tlc-sources', maxLength: 500, disabled }} />
                <ProFormText name={['tlcAssignment', 'example']} label={t('traceabilityPlan.tlcExample')} width="md" fieldProps={{ list: 'tlc-examples', maxLength: 500, disabled }} />
              </Space>
              <datalist id="tlc-sources">{status.context.tlcSources.map((s) => <option key={s.source} value={s.source} />)}</datalist>
              <datalist id="tlc-examples">{status.context.tlcSources.map((s) => <option key={s.example} value={s.example} />)}</datalist>
              {status.context.tlcSources.length > 0 && (
                <Typography.Paragraph type="secondary">
                  {t('traceabilityPlan.fromRecords')}: {status.context.tlcSources.map((s) => <Tag key={s.source}>{s.source} · {s.example}</Tag>)}
                </Typography.Paragraph>
              )}
            </StepsForm.StepForm>

            <StepsForm.StepForm name="s4" title={secTitle(4)} initialValues={{ pointOfContact: initial.pointOfContact }}>
              {help(4)}
              <ProFormTextArea name={['pointOfContact', 'text']} label={t('traceabilityPlan.description')} fieldProps={{ rows: 4, maxLength: 20000, disabled }} />
              <Space wrap align="start">
                <ProFormText name={['pointOfContact', 'name']} label={t('traceabilityPlan.contactName')} width="sm" fieldProps={{ maxLength: 500, disabled }} />
                <ProFormText name={['pointOfContact', 'role']} label={t('traceabilityPlan.contactRole')} width="sm" fieldProps={{ maxLength: 500, disabled }} />
                <ProFormText name={['pointOfContact', 'email']} label={t('traceabilityPlan.contactEmail')} width="md" rules={[{ type: 'email' }]} fieldProps={{ disabled }} />
                <ProFormText name={['pointOfContact', 'phone']} label={t('traceabilityPlan.contactPhone')} width="sm" fieldProps={{ maxLength: 500, disabled }} />
              </Space>
            </StepsForm.StepForm>

            <StepsForm.StepForm name="s5" title={secTitle(5)} initialValues={{ farmMap: initial.farmMap }}>
              {help(5)}
              <ProFormSwitch name={['farmMap', 'applicable']} label={t('traceabilityPlan.farmApplicable')} fieldProps={{ disabled }} />
              <ProFormDependency name={[['farmMap', 'applicable'], ['farmMap', 'plotIds']]}>
                {(deps: { farmMap?: { applicable?: boolean; plotIds?: string[] } }) =>
                  deps.farmMap?.applicable ? (
                    <>
                      <ProFormTextArea name={['farmMap', 'text']} label={t('traceabilityPlan.description')} fieldProps={{ rows: 4, maxLength: 20000, disabled }} />
                      <ProFormSelect name={['farmMap', 'plotIds']} label={t('traceabilityPlan.plots')} mode="multiple" options={plotOptions} fieldProps={{ disabled }} />
                      <PlotOverview plotIds={deps.farmMap?.plotIds ?? []} />
                    </>
                  ) : (
                    <Typography.Text type="secondary">{t('traceabilityPlan.farmNotApplicable')}</Typography.Text>
                  )
                }
              </ProFormDependency>
            </StepsForm.StepForm>

            <StepsForm.StepForm name="s6" title={secTitle(6)}>
              {canWrite ? (
                <ProFormTextArea
                  name="reason"
                  label={t('traceabilityPlan.reason')}
                  tooltip={t('traceabilityPlan.reasonHelp')}
                  rules={[{ required: true, whitespace: true }]}
                  fieldProps={{ rows: 3, maxLength: 1000 }}
                  extra={t('traceabilityPlan.reasonHelp')}
                />
              ) : (
                <Typography.Text type="secondary">{t('traceabilityPlan.readOnly')}</Typography.Text>
              )}
            </StepsForm.StepForm>
          </StepsForm>
        </Card>
      )}

      <Drawer title={t('traceabilityPlan.history')} open={historyOpen} onClose={() => setHistoryOpen(false)} size="large">
        <List
          loading={versions.isLoading}
          dataSource={versions.data?.items ?? []}
          locale={{ emptyText: <Empty description={t('common.empty')} /> }}
          renderItem={(v) => (
            <List.Item
              actions={[
                <Dropdown key="e" menu={exportMenu(v.version)}><a>{t('traceabilityPlan.export')}</a></Dropdown>,
                canWrite && (
                  <a key="r" onClick={() => { setSeed(v); setHistoryOpen(false); notifySuccess(t('traceabilityPlan.restored', { n: v.version })); }}>
                    {t('traceabilityPlan.restore')}
                  </a>
                ),
              ].filter(Boolean)}
            >
              <List.Item.Meta
                title={<Space>{t('traceabilityPlan.version', { n: v.version })}{v.current && <Tag color="green">{t('traceabilityPlan.current')}</Tag>}</Space>}
                description={<><div>{formatDateTime(v.createdAt)} · {t('traceabilityPlan.by')} {v.createdBy}</div><div>{v.reason}</div></>}
              />
            </List.Item>
          )}
        />
      </Drawer>
    </PageContainer>
  );
}
