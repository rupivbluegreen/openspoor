import { useState } from 'react';
import { Alert, Button, Modal, Table, Typography } from 'antd';
import { CloudUploadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import {
  api, ModalForm, PageContainer, ProFormDigit, ProFormText, ProFormTextArea, QueryTable, StatusTag,
  type ProColumns, type SensorExcursion, type SensorIngestResult, type SensorReading,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import { useSession } from '../../lib/session';

type Row = SensorExcursion & Record<string, unknown>;

interface IngestForm {
  deviceId: string;
  epc?: string;
  shipmentId?: string;
  minC?: number;
  maxC?: number;
  readings: string;
}

const SAMPLE = (() => {
  const t0 = dayjs().startOf('hour').subtract(2, 'hour');
  const temps = [4.1, 4.3, 5.0, 8.6, 9.4, 9.1, 7.2, 5.5, 4.8];
  return temps.map((c, i) => `${t0.add(i * 15, 'minute').toISOString()},${c}`).join('\n');
})();

/** Parses `time,tempC[,humidity]` lines (a header line is skipped). */
function parseReadings(text: string): SensorReading[] {
  const out: SensorReading[] = [];
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || /^time\b/i.test(line)) continue;
    const [time, temp, hum] = line.split(/[,;\t]/).map((s) => s.trim());
    const d = dayjs(time);
    if (!d.isValid()) throw new Error(`Invalid time: ${time}`);
    const num = (s?: string) => {
      if (s === undefined || s === '') return null;
      const n = Number(s);
      if (Number.isNaN(n)) throw new Error(`Invalid number: ${s}`);
      return n;
    };
    out.push({ time: d.toISOString(), tempC: num(temp), humidity: num(hum) });
  }
  return out;
}

const range = (r: SensorExcursion) => `${r.minC}–${r.maxC} °C`;

export default function ColdChain() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const [result, setResult] = useState<SensorIngestResult | null>(null);
  const ingest = useSubmit(
    (v: IngestForm) =>
      api.post<SensorIngestResult>('/sensors/readings', {
        deviceId: v.deviceId,
        epc: v.epc || undefined,
        shipmentId: v.shipmentId || undefined,
        minC: v.minC ?? undefined,
        maxC: v.maxC ?? undefined,
        readings: parseReadings(v.readings),
      }),
    { invalidate: [['/sensors/excursions'], ['/alerts']], onSuccess: (r) => setResult(r) },
  );

  const columns: ProColumns<Row>[] = [
    { title: 'Device', dataIndex: 'deviceId' },
    { title: t('field.epc'), dataIndex: 'epc', ellipsis: true, render: (_d, r) => (r.epc ? <Typography.Text code>{r.epc}</Typography.Text> : '–') },
    { title: 'Allowed', render: (_d, r) => range(r) },
    {
      title: 'Peak',
      dataIndex: 'peakC',
      render: (_d, r) => <Typography.Text type={r.peakC > r.maxC || r.peakC < r.minC ? 'danger' : undefined}>{r.peakC.toFixed(1)} °C</Typography.Text>,
    },
    { title: 'Duration', dataIndex: 'minutes', render: (_d, r) => `${r.minutes} min` },
    { title: 'Started', dataIndex: 'startedAt', valueType: 'dateTime' },
    { title: 'Ended', dataIndex: 'endedAt', valueType: 'dateTime' },
    { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
  ];

  return (
    <PageContainer title={t('nav.coldChain')} subTitle="Temperature excursions detected from sensor readings (latest 500)">
      <QueryTable<Row>
        rowKey="id"
        queryKey={['/sensors/excursions']}
        fetcher={() => api.list<Row>('/sensors/excursions')}
        scroll={{ x: 'max-content' }}
        columns={[...columns, { title: 'Shipment', dataIndex: 'shipmentId', ellipsis: true, render: (_d, r) => r.shipmentId ?? '–' }]}
        toolBarRender={() => [
          hasRole('Ops', 'Integration') ? (
            <ModalForm<IngestForm>
              key="ingest"
              title="Upload sensor readings"
              width={640}
              trigger={
                <Button type="primary" icon={<CloudUploadOutlined />}>
                  Upload readings
                </Button>
              }
              modalProps={{ destroyOnHidden: true }}
              initialValues={{ minC: 2, maxC: 8, readings: SAMPLE }}
              onFinish={ingest}
            >
              <ProFormText name="deviceId" label="Device ID" rules={[{ required: true }]} />
              <ProFormText name="epc" label={t('field.epc')} tooltip="Monitored object; a sensor event is captured on it" placeholder="urn:epc:id:sscc:… or GS1 Digital Link" />
              <ProFormText name="shipmentId" label="Shipment ID" rules={[{ pattern: /^[0-9a-f-]{36}$/i, message: 'UUID' }]} />
              <ProFormDigit name="minC" label="Min °C" fieldProps={{ step: 0.5 }} min={-100} max={100} />
              <ProFormDigit
                name="maxC"
                label="Max °C"
                fieldProps={{ step: 0.5 }}
                min={-100}
                max={100}
                dependencies={['minC']}
                rules={[
                  ({ getFieldValue }) => ({
                    validator: async (_r, v?: number) => {
                      const lo = getFieldValue('minC') as number | undefined;
                      if (v !== undefined && v !== null && lo !== undefined && lo !== null && lo >= v) throw new Error('Must be above min °C');
                    },
                  }),
                ]}
              />
              <ProFormTextArea
                name="readings"
                label="Readings"
                tooltip="One per line: time,tempC[,humidity] (ISO 8601 time)"
                fieldProps={{ rows: 10, style: { fontFamily: 'monospace' } }}
                rules={[
                  { required: true },
                  {
                    validator: async (_r, v: string) => {
                      const n = parseReadings(v ?? '').length;
                      if (n < 1 || n > 100_000) throw new Error('1–100000 readings required');
                    },
                  },
                ]}
              />
            </ModalForm>
          ) : null,
        ]}
      />
      <Modal
        open={!!result}
        title="Readings stored"
        width={760}
        onCancel={() => setResult(null)}
        onOk={() => setResult(null)}
        cancelButtonProps={{ style: { display: 'none' } }}
      >
        {result && (
          <>
            <Alert
              type={result.excursions.length ? 'error' : 'success'}
              showIcon
              title={`${result.stored} reading(s) stored · ${result.excursions.length} excursion(s) detected`}
              description={result.excursions.length ? 'An alert was raised for each excursion.' : undefined}
              style={{ marginBottom: 12 }}
            />
            {!!result.excursions.length && (
              <Table<SensorExcursion>
                size="small"
                rowKey="id"
                pagination={false}
                dataSource={result.excursions}
                columns={[
                  { title: 'Allowed', render: (_d, r) => range(r) },
                  { title: 'Peak', render: (_d, r) => `${r.peakC.toFixed(1)} °C` },
                  { title: 'Duration', render: (_d, r) => `${r.minutes} min` },
                  { title: 'Started', render: (_d, r) => dayjs(r.startedAt).format('YYYY-MM-DD HH:mm') },
                  { title: 'Ended', render: (_d, r) => dayjs(r.endedAt).format('YYYY-MM-DD HH:mm') },
                ]}
              />
            )}
          </>
        )}
      </Modal>
    </PageContainer>
  );
}
