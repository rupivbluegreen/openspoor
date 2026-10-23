import { Alert, Card, Descriptions, Space, Tag, Typography } from 'antd';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, ModalForm, PageContainer, ProFormCheckbox, ProFormDatePicker, ProFormSelect, ProFormText, QueryTable, notifyError, notifySuccess,
  type ProColumns,
} from '@openspoor/ui/pro';
import { Button } from 'antd';
import dayjs from 'dayjs';
import { useSession } from '../../lib/session';

interface Reason { code: string; severity: 'error' | 'warning' | 'info'; message: string }
interface PlotReport {
  plotId: string; name: string; commodity: string; geometryType: string; status: 'valid' | 'warnings' | 'invalid';
  areaHa: number; decimals: number; reasons: Reason[]; overlaps: { plotId: string; name: string; kind: string }[];
}
interface Settings { ddsRole: 'operator' | 'downstream_trader'; roles: string[]; notes: string[] }
interface RefDate { key: string; label: string; date: string; status: string; verified: boolean; note: string; source: string; daysUntil: number }

const STATUS_COLOR = { valid: 'green', warnings: 'orange', invalid: 'red' } as const;
const SEV_COLOR = { error: 'red', warning: 'orange', info: 'blue' } as const;

export default function EudrReadiness() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const qc = useQueryClient();
  const canWrite = hasRole('Admin', 'Ops', 'Compliance');
  const settings = useQuery({ queryKey: ['/food/eudr/settings'], queryFn: () => api.get<Settings>('/food/eudr/settings') });
  const dates = useQuery({ queryKey: ['/food/eudr/dates'], queryFn: () => api.list<RefDate>('/food/eudr/dates') });

  const save = async (fn: () => Promise<unknown>, key: string) => {
    try {
      await fn();
      notifySuccess(t('common.success'));
      await qc.invalidateQueries({ queryKey: [key] });
      return true;
    } catch (e) {
      notifyError(e);
      return false;
    }
  };

  const columns: ProColumns<PlotReport & Record<string, unknown>>[] = [
    { title: t('field.name'), dataIndex: 'name' },
    { title: t('field.commodity'), dataIndex: 'commodity' },
    { title: 'Geometry', dataIndex: 'geometryType' },
    { title: 'Area (ha)', dataIndex: 'areaHa' },
    { title: 'Decimals', dataIndex: 'decimals' },
    { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <Tag color={STATUS_COLOR[r.status]}>{r.status}</Tag> },
    {
      title: 'Reasons',
      render: (_d, r) => (
        <Space size={[4, 4]} wrap>
          {r.reasons.map((x) => (
            <Tag key={x.code} color={SEV_COLOR[x.severity]} title={x.message}>
              {x.code}
            </Tag>
          ))}
        </Space>
      ),
    },
  ];

  return (
    <PageContainer title={t('nav.eudrReadiness')} subTitle="Role, application dates and plot geolocation checks (Regulation (EU) 2023/1115)">
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        title="Working defaults, not legal advice"
        description="The statement role, the application dates and the geolocation thresholds are configurable or marked TODO(spec); check the Official Journal."
      />
      <Card title="Statement role" style={{ marginBottom: 16 }} loading={settings.isLoading}>
        <Descriptions column={1} size="small">
          <Descriptions.Item label="This organisation acts as">
            <Tag>{settings.data?.ddsRole === 'downstream_trader' ? 'downstream trader (holds the upstream statement reference)' : 'operator (creates statements)'}</Tag>
            {canWrite && settings.data ? (
              <ModalForm<{ ddsRole: string; reason?: string }>
                title="Statement role"
                trigger={<Button size="small">Change</Button>}
                modalProps={{ destroyOnHidden: true }}
                initialValues={{ ddsRole: settings.data.ddsRole }}
                onFinish={(v) => save(() => api.put('/food/eudr/settings', v), '/food/eudr/settings')}
              >
                <ProFormSelect name="ddsRole" label="Role" options={settings.data.roles} rules={[{ required: true }]} />
                <ProFormText name="reason" label="Reason" />
              </ModalForm>
            ) : null}
          </Descriptions.Item>
        </Descriptions>
        {settings.data?.notes.map((n) => (
          <Typography.Paragraph key={n} type="secondary" style={{ margin: 0 }}>
            {n}
          </Typography.Paragraph>
        ))}
      </Card>
      <Card title="Application dates" style={{ marginBottom: 16 }} loading={dates.isLoading}>
        {dates.data?.items.map((d) => (
          <Descriptions key={d.key} column={1} size="small" title={d.label} style={{ marginBottom: 12 }}>
            <Descriptions.Item label="Date">
              {dayjs(d.date).format('YYYY-MM-DD')} ({d.daysUntil} days) <Tag color={d.verified ? 'green' : 'orange'}>{d.status}</Tag>
              {canWrite ? (
                <ModalForm<{ date: string; note?: string; verified?: boolean }>
                  title={d.label}
                  trigger={<Button size="small">Edit</Button>}
                  modalProps={{ destroyOnHidden: true }}
                  initialValues={{ date: d.date, note: d.note, verified: d.verified }}
                  onFinish={(v) => save(() => api.put(`/food/eudr/dates/${d.key}`, { ...v, date: dayjs(v.date).format('YYYY-MM-DD') }), '/food/eudr/dates')}
                >
                  <ProFormDatePicker name="date" label="Date" rules={[{ required: true }]} />
                  <ProFormText name="note" label="Note" fieldProps={{ maxLength: 500 }} />
                  <ProFormCheckbox name="verified">Confirmed against the Official Journal</ProFormCheckbox>
                </ModalForm>
              ) : null}
            </Descriptions.Item>
            <Descriptions.Item label="Source">{d.source}</Descriptions.Item>
          </Descriptions>
        ))}
      </Card>
      <QueryTable<PlotReport & Record<string, unknown>>
        rowKey="plotId"
        headerTitle="Plot geolocation validation"
        queryKey={['/food/eudr/plots/validation']}
        fetcher={() => api.list<PlotReport & Record<string, unknown>>('/food/eudr/plots/validation')}
        columns={columns}
        scroll={{ x: 'max-content' }}
      />
    </PageContainer>
  );
}
