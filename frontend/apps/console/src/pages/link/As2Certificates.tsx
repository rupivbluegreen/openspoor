import { Alert, Button, Card, Col, Descriptions, Popconfirm, Row, Space, Statistic, Table, Tag, Tooltip, Typography } from 'antd';
import { SafetyCertificateOutlined, SwapOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import { api, ModalForm, PageContainer, ProFormDateTimePicker, ProFormSelect, ProFormSwitch, ProFormTextArea } from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';
import { useSession } from '../../lib/session';
import type { As2CertView, As2Options, As2Overview, As2PartnerCerts, As2Warning } from './linkTypes';

const KEY = ['/as2/certificates'] as const;
const fmt = (s?: string) => (s ? dayjs(s).format('YYYY-MM-DD HH:mm') : '–');
const STAGE_COLOR: Record<string, string> = { ok: 'green', '60d': 'gold', '30d': 'orange', '7d': 'red', expired: 'red' };
const SEVERITY_COLOR: Record<string, string> = { info: 'blue', warning: 'orange', error: 'red' };
const DIGESTS = ['sha-256', 'sha-384', 'sha-512', 'sha-1'];

function CertCell({ cert }: { cert?: As2CertView }) {
  const { t } = useTranslation();
  if (!cert) return <>–</>;
  return (
    <Space direction="vertical" size={0}>
      <Tooltip title={cert.fingerprint}>
        <Typography.Text ellipsis style={{ maxWidth: 240 }}>{cert.subject}</Typography.Text>
      </Tooltip>
      <Typography.Text type="secondary">{fmt(cert.notBefore)} – {fmt(cert.notAfter)}</Typography.Text>
      <Tag color={STAGE_COLOR[cert.stage] ?? 'default'}>
        {cert.stage === 'expired' ? t('as2.expired') : t('as2.daysLeft', { days: cert.daysLeft })}
      </Tag>
    </Space>
  );
}

function Warnings({ items }: { items: As2Warning[] }) {
  if (!items.length) return <Tag color="green">OK</Tag>;
  return (
    <Space size={[4, 4]} wrap>
      {items.map((w) => (
        <Tooltip key={w.code} title={w.message}>
          <Tag color={SEVERITY_COLOR[w.severity] ?? 'default'}>{w.code}</Tag>
        </Tooltip>
      ))}
    </Space>
  );
}

function OptionsForm({ row, canWrite }: { row: As2PartnerCerts; canWrite: boolean }) {
  const { t } = useTranslation();
  const save = useSubmit((v: As2Options) => api.put(`/partners/${row.partnerId}/as2/options`, v), { invalidate: [KEY] });
  return (
    <ModalForm<As2Options>
      title={`${t('as2.options')} – ${row.name}`}
      trigger={<Button size="small" disabled={!canWrite}>{t('as2.options')}</Button>}
      modalProps={{ destroyOnHidden: true }}
      initialValues={row.options}
      onFinish={save}
    >
      <ProFormSelect name="signatureAlgorithm" label={t('as2.signatureAlgorithm')} options={DIGESTS.map((d) => ({ value: d, label: d.toUpperCase() }))} />
      <ProFormSelect name="mdnSignatureAlgorithm" label={t('as2.mdnSignatureAlgorithm')} options={DIGESTS.map((d) => ({ value: d, label: d.toUpperCase() }))} />
      <ProFormSelect name="encryption" label={t('as2.encryption')} options={[{ value: 'aes-256-cbc', label: 'AES-256-CBC' }, { value: 'aes-128-cbc', label: 'AES-128-CBC' }]} />
      <ProFormSwitch name="compress" label={t('as2.compress')} />
      <ProFormSwitch name="legacy" label={t('as2.legacy')} tooltip={t('as2.legacyHint')} />
    </ModalForm>
  );
}

function NextForm({ row, canWrite }: { row: As2PartnerCerts; canWrite: boolean }) {
  const { t } = useTranslation();
  const save = useSubmit((v: { certPem: string; switchAt?: string }) => api.put(`/partners/${row.partnerId}/as2/next`, v), { invalidate: [KEY] });
  const cancel = useAction(() => api.del(`/partners/${row.partnerId}/as2/next`), { invalidate: [KEY] });
  return (
    <Space size={4}>
      <ModalForm<{ certPem: string; switchAt?: string }>
        title={`${t('as2.nextCertificate')} – ${row.name}`}
        trigger={<Button size="small" icon={<SwapOutlined />} disabled={!canWrite}>{t('as2.rotate')}</Button>}
        modalProps={{ destroyOnHidden: true }}
        onFinish={save}
      >
        <ProFormTextArea name="certPem" label={t('as2.certificatePem')} rules={[{ required: true }]} fieldProps={{ rows: 8, style: { fontFamily: 'monospace' } }} />
        <ProFormDateTimePicker name="switchAt" label={t('as2.switchAt')} tooltip={t('as2.switchHint')} />
      </ModalForm>
      {row.next && canWrite && (
        <Popconfirm title={t('as2.cancelRotation')} onConfirm={() => cancel.mutate(undefined)}>
          <Button size="small" danger loading={cancel.isPending}>{t('action.cancel')}</Button>
        </Popconfirm>
      )}
    </Space>
  );
}

export default function As2Certificates() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canWrite = hasRole('Admin', 'Integration');
  const res = useQuery({ queryKey: KEY, queryFn: () => api.get<As2Overview>('/as2/certificates') });
  const rotate = useSubmit((v: { switchAt?: string }) => api.put('/as2/station/next', { generate: true, switchAt: v.switchAt }), { invalidate: [KEY] });
  const cancel = useAction(() => api.del('/as2/station/next'), { invalidate: [KEY] });
  const ov = res.data;
  const st = ov?.station;
  return (
    <PageContainer title={t('nav.as2Certificates')} subTitle={t('as2.subtitle')} loading={res.isLoading}>
      <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        {!!ov?.summary.legacy && <Alert type="warning" showIcon message={t('as2.legacyWarning', { n: ov.summary.legacy })} />}
        {!!ov?.summary.expired && <Alert type="error" showIcon message={t('as2.expiredWarning', { n: ov.summary.expired })} />}
        <Row gutter={16}>
          <Col xs={12} md={6}><Card size="small"><Statistic title={t('as2.partners')} value={ov?.summary.partners ?? 0} /></Card></Col>
          <Col xs={12} md={6}><Card size="small"><Statistic title={t('as2.expiring')} value={ov?.summary.expiring ?? 0} /></Card></Col>
          <Col xs={12} md={6}><Card size="small"><Statistic title={t('as2.expired')} value={ov?.summary.expired ?? 0} /></Card></Col>
          <Col xs={12} md={6}><Card size="small"><Statistic title={t('as2.rotations')} value={ov?.summary.rotations ?? 0} /></Card></Col>
        </Row>
        <Card
          size="small"
          title={<Space><SafetyCertificateOutlined />{t('as2.station')}</Space>}
          extra={
            st && (
              <Space>
                <ModalForm<{ switchAt?: string }>
                  title={t('as2.rotateStation')}
                  trigger={<Button type="primary" icon={<SwapOutlined />} disabled={!canWrite}>{t('as2.rotate')}</Button>}
                  modalProps={{ destroyOnHidden: true }}
                  onFinish={rotate}
                >
                  <Typography.Paragraph type="secondary">{t('as2.rotateStationHint')}</Typography.Paragraph>
                  <ProFormDateTimePicker name="switchAt" label={t('as2.switchAt')} tooltip={t('as2.switchHint')} />
                </ModalForm>
                {st.next && canWrite && (
                  <Popconfirm title={t('as2.cancelRotation')} onConfirm={() => cancel.mutate(undefined)}>
                    <Button danger loading={cancel.isPending}>{t('action.cancel')}</Button>
                  </Popconfirm>
                )}
              </Space>
            )
          }
        >
          {st ? (
            <Descriptions size="small" column={{ xs: 1, md: 2 }} bordered>
              <Descriptions.Item label="AS2 ID">{st.as2Id}</Descriptions.Item>
              <Descriptions.Item label={t('as2.receiveUrl')}><Typography.Text copyable>{st.receiveUrl}</Typography.Text></Descriptions.Item>
              <Descriptions.Item label={t('as2.currentCertificate')}><CertCell cert={st.current} /></Descriptions.Item>
              <Descriptions.Item label={t('as2.nextCertificate')}>
                {st.next ? <Space direction="vertical" size={0}><CertCell cert={st.next} /><Typography.Text type="secondary">{t('as2.switchesOn', { date: fmt(st.switchAt) })}</Typography.Text></Space> : '–'}
              </Descriptions.Item>
              <Descriptions.Item label={t('as2.signingWith')}>{st.active === 'next' ? t('as2.nextCertificate') : t('as2.currentCertificate')}</Descriptions.Item>
              <Descriptions.Item label={t('as2.warnings')}><Warnings items={st.warnings} /></Descriptions.Item>
            </Descriptions>
          ) : (
            <Alert type="info" showIcon message={t('as2.noStation')} />
          )}
        </Card>
        <Card size="small" title={t('as2.partnerCertificates')}>
          <Table<As2PartnerCerts>
            size="small"
            rowKey="partnerId"
            loading={res.isFetching}
            dataSource={ov?.partners ?? []}
            scroll={{ x: true }}
            columns={[
              { title: t('field.partner'), dataIndex: 'name', render: (v: string, r) => <Space direction="vertical" size={0}><span>{v}</span><Typography.Text type="secondary">{r.as2Id}</Typography.Text></Space> },
              { title: t('as2.currentCertificate'), dataIndex: 'current', render: (c: As2CertView) => <CertCell cert={c} /> },
              { title: t('as2.nextCertificate'), dataIndex: 'next', render: (c: As2CertView | undefined, r) => (c ? <Space direction="vertical" size={0}><CertCell cert={c} /><Typography.Text type="secondary">{t('as2.switchesOn', { date: fmt(r.switchAt) })}</Typography.Text></Space> : '–') },
              {
                title: t('as2.options'),
                dataIndex: 'options',
                render: (o: As2Options) => (
                  <Space size={[4, 4]} wrap>
                    <Tag>{o.signatureAlgorithm.toUpperCase()}</Tag>
                    <Tag>{o.encryption.toUpperCase()}</Tag>
                    {o.compress && <Tag>{t('as2.compress')}</Tag>}
                    {o.legacy && <Tag color="orange">{t('as2.legacy')}</Tag>}
                  </Space>
                ),
              },
              { title: t('as2.warnings'), dataIndex: 'warnings', render: (w: As2Warning[]) => <Warnings items={w} /> },
              { title: t('common.actions'), render: (_v, r) => <Space size={4} wrap><NextForm row={r} canWrite={canWrite} /><OptionsForm row={r} canWrite={canWrite} /></Space> },
            ]}
          />
        </Card>
        <Typography.Text type="secondary">{t('as2.thresholds', { days: (ov?.thresholdsDays ?? []).join(' / ') })}</Typography.Text>
      </Space>
    </PageContainer>
  );
}
