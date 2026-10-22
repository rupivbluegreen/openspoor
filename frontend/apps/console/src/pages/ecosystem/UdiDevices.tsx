import { useState } from 'react';
import { Alert, Descriptions, Modal, Popconfirm, Space, Tag, Tooltip, Typography } from 'antd';
import { CheckOutlined, SendOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, formatDateTime, gtinValidator, ProFormDependency, ProFormSelect, ProFormSwitch, ProFormText, ProFormTextArea,
  type UdiDevice, type UdiSubmitResult,
} from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { useAction } from '../../lib/hooks';
import { useSession } from '../../lib/session';

type Row = UdiDevice & Record<string, unknown>;
type Target = 'gudid' | 'eudamed';

const RISK_CLASSES = ['US-I', 'US-II', 'US-III', 'EU-I', 'EU-IIa', 'EU-IIb', 'EU-III'];
const TARGET_LABEL: Record<Target, string> = { gudid: 'GUDID (FDA)', eudamed: 'EUDAMED (EU)' };
const targetFor = (riskClass: string): Target | null => (riskClass.startsWith('US-') ? 'gudid' : riskClass.startsWith('EU-') ? 'eudamed' : null);

function SubmitLink({ device, onDone }: { device: Row; onDone: (r: UdiSubmitResult & { target: Target }) => void }) {
  const target = targetFor(device.riskClass);
  const submit = useAction((tg: Target) => api.post<UdiSubmitResult>(`/udi/devices/${device.id}/submit?target=${tg}`), {
    invalidate: [['/udi/devices']],
    onSuccess: (r, tg) => onDone({ ...r, target: tg }),
  });
  if (!target) return null;
  const done = !!device.submissions?.[target];
  return (
    <Popconfirm
      key="submit"
      title={`Prepare ${TARGET_LABEL[target]} submission?`}
      description="Validates required fields and builds the payload. Transmission is stubbed until gateway onboarding."
      onConfirm={() => submit.mutateAsync(target)}
    >
      <a>
        <SendOutlined /> {done ? 'Resubmit' : 'Submit'} → {target.toUpperCase()}
      </a>
    </Popconfirm>
  );
}

export default function UdiDevices() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canEdit = hasRole('Compliance');
  const [result, setResult] = useState<(UdiSubmitResult & { target: Target }) | null>(null);

  return (
    <>
      <CrudPage<Row>
        title={t('nav.udiDevices')}
        subTitle="Medical device UDI records (GS1 device identifiers) for GUDID and EUDAMED"
        path="/udi/devices"
        columns={[
          { title: 'DI (GTIN)', dataIndex: 'di', render: (_d, r) => <Typography.Text code>{r.di}</Typography.Text> },
          { title: 'Brand name', dataIndex: 'brandName' },
          { title: 'Model', dataIndex: 'model', render: (_d, r) => [r.model, r.version].filter(Boolean).join(' · ') || '–' },
          { title: 'Risk class', dataIndex: 'riskClass', render: (_d, r) => <Tag color={r.riskClass.startsWith('US-') ? 'blue' : 'purple'}>{r.riskClass}</Tag> },
          { title: 'Labeler', dataIndex: 'labeler' },
          { title: 'GMDN / EMDN', render: (_d, r) => [r.gmdnCode, r.emdnCode].filter(Boolean).join(' / ') || '–' },
          {
            title: 'Flags',
            render: (_d, r) => (
              <Space size={2} wrap>
                {r.sterile && <Tag>sterile</Tag>}
                {r.singleUse && <Tag>single use</Tag>}
                {r.rx && <Tag>Rx</Tag>}
              </Space>
            ),
          },
          {
            title: 'Submissions',
            dataIndex: 'submissions',
            render: (_d, r) => {
              const subs = Object.entries(r.submissions ?? {});
              if (!subs.length) return <Typography.Text type="secondary">none</Typography.Text>;
              return subs.map(([k, s]) => (
                <Tooltip key={k} title={`${s.ref} · ${formatDateTime(s.at)}${s.note ? `\n${s.note}` : ''}`}>
                  <Tag icon={<CheckOutlined />} color="processing">
                    {k}: {s.status}
                  </Tag>
                </Tooltip>
              ));
            },
          },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
        ]}
        rowActions={canEdit ? (r) => [<SubmitLink key="submit" device={r} onDone={setResult} />] : undefined}
        create={
          canEdit
            ? {
                width: 640,
                initialValues: { riskClass: 'EU-IIa', sterile: false, singleUse: false, rx: false },
                fields: (
                  <>
                    <ProFormText name="di" label="Device identifier (GTIN)" rules={[{ required: true }, gtinValidator()]} />
                    <ProFormText name="brandName" label="Brand name" rules={[{ required: true }]} />
                    <ProFormText name="labeler" label="Labeler / manufacturer" rules={[{ required: true }]} />
                    <ProFormSelect name="riskClass" label="Risk class" rules={[{ required: true }]} options={RISK_CLASSES.map((c) => ({ value: c, label: c }))} />
                    <ProFormText name="model" label="Model" />
                    <ProFormText name="version" label={t('field.version')} />
                    <ProFormTextArea name="description" label={t('field.description')} />
                    <ProFormDependency name={['riskClass']}>
                      {({ riskClass }: { riskClass?: string }) =>
                        String(riskClass ?? '').startsWith('US-') ? (
                          <ProFormText name="gmdnCode" label="GMDN code" tooltip="Required for GUDID" />
                        ) : (
                          <>
                            <ProFormText name="basicUdiDi" label="Basic UDI-DI" tooltip="Required for EUDAMED" />
                            <ProFormText name="emdnCode" label="EMDN code" tooltip="Required for EUDAMED" />
                          </>
                        )
                      }
                    </ProFormDependency>
                    <Space size={24}>
                      <ProFormSwitch name="sterile" label="Sterile" />
                      <ProFormSwitch name="singleUse" label="Single use" />
                      <ProFormSwitch name="rx" label="Prescription (Rx)" />
                    </Space>
                  </>
                ),
              }
            : undefined
        }
      />
      <Modal
        open={!!result}
        title={result ? `${TARGET_LABEL[result.target]} submission prepared` : ''}
        width={720}
        onCancel={() => setResult(null)}
        onOk={() => setResult(null)}
        cancelButtonProps={{ style: { display: 'none' } }}
      >
        {result && (
          <>
            <Alert type="info" showIcon title={result.submission.status} description={result.submission.note} style={{ marginBottom: 12 }} />
            <Descriptions size="small" column={2} style={{ marginBottom: 12 }}>
              <Descriptions.Item label="Reference">
                <Typography.Text code copyable>
                  {result.submission.ref}
                </Typography.Text>
              </Descriptions.Item>
              <Descriptions.Item label={t('field.at')}>{formatDateTime(result.submission.at)}</Descriptions.Item>
            </Descriptions>
            <CodeView code={result.payload} maxHeight={360} filename={`${result.target}-${result.submission.ref}.xml`} mime="application/xml" />
          </>
        )}
      </Modal>
    </>
  );
}
