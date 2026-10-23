import { useState } from 'react';
import { Alert, Button, Card, Col, Empty, Popconfirm, Row, Segmented, Space, Statistic, Tabs, Tag, Tooltip, Typography } from 'antd';
import { ArrowDownOutlined, ArrowRightOutlined, ArrowUpOutlined, CheckCircleOutlined, CloseCircleOutlined, EditOutlined, TeamOutlined } from '@ant-design/icons';
import { Line } from '@ant-design/charts';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  api, formatKpiValue, groupKpis, hasSparkline, ModalForm, PageContainer, ProFormDigit, ProFormText, trend, trendIsGood, useAppSettings,
  type KpiItem, type KpiList, type KpiSeries,
} from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';
import { useSession } from '../../lib/session';

const WINDOWS = [7, 30, 90];

interface GoalForm {
  target: number;
  note?: string;
}

function GoalControl({ k, canEdit }: { k: KpiItem; canEdit: boolean }) {
  const { t } = useTranslation();
  const save = useSubmit((v: GoalForm) => api.put(`/kpis/${k.id}/goal`, { target: v.target, note: v.note ?? '' }), { invalidate: [['/kpis']], success: t('kpi.saved') });
  const remove = useAction(() => api.del(`/kpis/${k.id}/goal`), { invalidate: [['/kpis']], success: t('kpi.removed') });
  const tag =
    k.goalStatus === 'met' ? (
      <Tag color="success" icon={<CheckCircleOutlined />}>
        {t('kpi.goalMet')} · {formatKpiValue(k.goal?.target, k.unit)}
      </Tag>
    ) : k.goalStatus === 'missed' ? (
      <Tag color="error" icon={<CloseCircleOutlined />}>
        {t('kpi.goalMissed')} · {formatKpiValue(k.goal?.target, k.unit)}
      </Tag>
    ) : k.goal ? (
      <Tag>
        {t('kpi.goal')} · {formatKpiValue(k.goal.target, k.unit)}
      </Tag>
    ) : (
      <Tag>{t('kpi.noGoal')}</Tag>
    );
  return (
    <Space size={4} wrap>
      {k.goal?.note ? <Tooltip title={k.goal.note}>{tag}</Tooltip> : tag}
      {canEdit && (
        <ModalForm<GoalForm>
          title={`${t('kpi.goal')} · ${t(`kpi.name.${k.id}`, { defaultValue: k.name })}`}
          trigger={
            <Button size="small" type="link" icon={<EditOutlined />}>
              {k.goal ? t('kpi.editGoal') : t('kpi.setGoal')}
            </Button>
          }
          modalProps={{ destroyOnHidden: true }}
          initialValues={{ target: k.goal?.target, note: k.goal?.note }}
          onFinish={save}
        >
          <ProFormDigit
            name="target"
            label={`${t('kpi.target')} (${k.unit})`}
            extra={k.direction === 'lower' ? t('kpi.targetHintLower') : t('kpi.targetHint')}
            min={0}
            max={k.unit === '%' ? 100 : undefined}
            rules={[{ required: true }]}
          />
          <ProFormText name="note" label={t('kpi.note')} />
        </ModalForm>
      )}
      {canEdit && k.goal && (
        <Popconfirm title={t('kpi.removeGoal')} onConfirm={() => remove.mutate(undefined)}>
          <Button size="small" type="link" danger>
            {t('kpi.removeGoal')}
          </Button>
        </Popconfirm>
      )}
    </Space>
  );
}

function Sparkline({ k, days }: { k: KpiItem; days: number }) {
  const { t } = useTranslation();
  const { mode } = useAppSettings();
  const q = useQuery({
    queryKey: ['/kpis', k.id, 'series', days],
    queryFn: () => api.get<KpiSeries>(`/kpis/${k.id}/series`, { days }),
    enabled: k.seriesAvailable,
    meta: { silent: true },
  });
  if (!k.seriesAvailable) return <Typography.Text type="secondary">{t('kpi.snapshot')} · {t('kpi.noHistory')}</Typography.Text>;
  const points = q.data?.points ?? [];
  const tr = trend(points);
  const good = trendIsGood(k.direction, tr);
  const icon = tr === 'up' ? <ArrowUpOutlined /> : tr === 'down' ? <ArrowDownOutlined /> : <ArrowRightOutlined />;
  return (
    <div>
      {hasSparkline(points) ? (
        <Line
          data={points.map((p) => ({ day: p.day, value: p.value }))}
          xField="day"
          yField="value"
          height={56}
          axis={false}
          legend={false}
          theme={mode === 'dark' ? 'classicDark' : 'classic'}
          style={{ lineWidth: 2 }}
          tooltip={{ items: [{ channel: 'y', name: k.unit }] }}
        />
      ) : (
        <div style={{ height: 56, display: 'flex', alignItems: 'center' }}>
          <Typography.Text type="secondary">{q.isLoading ? '…' : t('kpi.noData')}</Typography.Text>
        </div>
      )}
      {tr && (
        <Tag color={good === null ? 'default' : good ? 'success' : 'warning'} icon={icon}>
          {tr === 'up' ? t('kpi.trendUp') : tr === 'down' ? t('kpi.trendDown') : t('kpi.trendFlat')}
        </Tag>
      )}
    </div>
  );
}

function KpiCard({ k, days, canEdit }: { k: KpiItem; days: number; canEdit: boolean }) {
  const { t } = useTranslation();
  const name = t(`kpi.name.${k.id}`, { defaultValue: k.name });
  return (
    <Card
      size="small"
      title={<Tooltip title={k.description}>{name}</Tooltip>}
      extra={<Tag>{k.direction === 'lower' ? t('kpi.lower') : t('kpi.higher')}</Tag>}
      style={{ height: '100%' }}
    >
      {k.error ? (
        <Alert type="warning" showIcon title={t('kpi.cannotCompute')} description={k.error} />
      ) : (
        <Space direction="vertical" size={8} style={{ width: '100%' }}>
          <Statistic value={formatKpiValue(k.value, k.unit)} />
          {k.value === null && <Typography.Text type="secondary">{t('kpi.noData')}</Typography.Text>}
          {k.kind !== 'snapshot' && k.value !== null && k.kind !== 'sum' && <Typography.Text type="secondary">{t('kpi.samples', { n: k.samples })}</Typography.Text>}
          {k.breakdown && k.breakdown.length > 0 && (
            <Space size={[4, 4]} wrap aria-label={t('kpi.breakdown')}>
              {k.breakdown.map((b) => (
                <Tag key={b.label}>
                  {b.label}: {b.count}
                </Tag>
              ))}
            </Space>
          )}
          <Sparkline k={k} days={days} />
          <GoalControl k={k} canEdit={canEdit} />
          {k.peerComparable && (
            <Link to="/ecosystem/benchmarks">
              <TeamOutlined /> {t('kpi.peerLink')}
            </Link>
          )}
        </Space>
      )}
    </Card>
  );
}

export default function KpiLibrary() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const [days, setDays] = useState(30);
  const [tab, setTab] = useState<string>();
  const q = useQuery({ queryKey: ['/kpis', days], queryFn: () => api.get<KpiList>('/kpis', { days }), refetchInterval: 60_000 });
  const items = q.data?.items ?? [];
  const groups = groupKpis(items);
  const categories = (q.data?.categories ?? []).filter((c) => c.count > 0);
  const active = tab && groups[tab] ? tab : categories[0]?.key;
  const canEdit = hasRole('Ops', 'Compliance');

  return (
    <PageContainer
      title={t('nav.kpiLibrary')}
      subTitle={t('kpi.subtitle')}
      extra={[
        <Space key="window">
          <Typography.Text type="secondary">{t('kpi.window')}</Typography.Text>
          <Segmented value={days} onChange={(v) => setDays(Number(v))} options={WINDOWS.map((d) => ({ value: d, label: t('kpi.days', { n: d }) }))} />
        </Space>,
      ]}
    >
      {q.isLoading ? (
        <Card loading />
      ) : !items.length ? (
        <Card>
          <Empty description={t('common.empty')} />
        </Card>
      ) : (
        <Tabs
          activeKey={active}
          onChange={setTab}
          items={categories.map((c) => ({
            key: c.key,
            label: `${t(`kpi.category.${c.key}`, { defaultValue: c.name })} (${c.count})`,
            children: (
              <Row gutter={[16, 16]}>
                {(groups[c.key] ?? []).map((k) => (
                  <Col key={k.id} xs={24} md={12} xl={8} xxl={6}>
                    <KpiCard k={k} days={days} canEdit={canEdit} />
                  </Col>
                ))}
              </Row>
            ),
          }))}
        />
      )}
    </PageContainer>
  );
}
