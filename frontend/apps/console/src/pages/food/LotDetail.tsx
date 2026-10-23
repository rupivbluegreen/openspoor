import { Alert, Button, Card, Empty, Popconfirm, Space, Table, Tag, Typography } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { Link, useParams } from 'react-router';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import {
  api, downloadBlob, glnValidator, ModalForm, notifyError, PageContainer, ProDescriptions, ProFormDatePicker, ProFormDependency, ProFormRadio,
  ProFormSelect, ProFormText, ProFormTextArea, ProFormUploadButton, type FoodCertCoverage, type FoodCertStatus,
} from '@openspoor/ui/pro';
import { useAction, useSubmit } from '../../lib/hooks';
import { usePartnerOptions } from '../../lib/options';

interface LotRow {
  tlc: string;
  description?: string;
  productId?: string;
  gtin?: string;
  quantity?: number;
  uom?: string;
  status?: string;
  origin?: string;
  locationGln?: string;
  tlcSource?: string;
  harvestDate?: string;
  packDate?: string;
  [k: string]: unknown;
}

interface Trace {
  sources: string[];
  derived: string[];
  events: { id: string; type: string; tlc: string; eventTime: string; quantity: number; uom: string }[];
}

/** Certificate kinds offered in the form; any other kind can be typed. Rules of certification bodies are not modelled. */
const KINDS = ['halal', 'origin', 'organic', 'kosher', 'gmp', 'iso 22000', 'fssc 22000', 'brcgs', 'globalg.a.p.'];

const STATUS_COLOR: Record<FoodCertStatus, string> = { valid: 'success', expiring: 'warning', expired: 'error', not_yet_valid: 'default' };

export function CertStatusTag({ c }: { c: Pick<FoodCertCoverage, 'status' | 'validTo'> }) {
  const { t } = useTranslation();
  return (
    <Tag color={STATUS_COLOR[c.status]}>
      {t(`foodCert.status_${c.status}`)}
      {c.validTo ? ` · ${dayjs(c.validTo).format('YYYY-MM-DD')}` : ''}
    </Tag>
  );
}

async function toBase64(file: Blob): Promise<string> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

type CertForm = {
  kind: string[] | string;
  number?: string;
  issuer?: string;
  scope?: string;
  validFrom?: string;
  validTo?: string;
  partnerId?: string;
  notes?: string;
  linkTo: 'lot' | 'product' | 'site';
  gln?: string;
  document?: { originFileObj?: File; name: string; type?: string }[];
};

export default function LotDetail() {
  const { tlc = '' } = useParams();
  const { t } = useTranslation();
  const partners = usePartnerOptions();
  const lots = useQuery({ queryKey: ['/food/lots'], queryFn: () => api.list<LotRow>('/food/lots') });
  const lot = lots.data?.items.find((l) => l.tlc === tlc);
  const certKey = ['/food/lots', tlc, 'certificates'];
  const certs = useQuery({ queryKey: certKey, queryFn: () => api.get<{ certificates: FoodCertCoverage[] }>(`/food/lots/${encodeURIComponent(tlc)}/certificates`) });
  const trace = useQuery({ queryKey: ['/food/lots', tlc, 'trace'], queryFn: () => api.get<Trace>(`/food/lots/${encodeURIComponent(tlc)}/trace`) });

  const add = useSubmit(
    async (v: CertForm) => {
      let documentId: string | undefined;
      const f = v.document?.[0]?.originFileObj;
      if (f) {
        const d = await api.post<{ id: string }>('/documents', { title: f.name, contentType: f.type || undefined, content: await toBase64(f) });
        documentId = d.id;
      }
      const target = v.linkTo === 'lot' ? tlc : v.linkTo === 'product' ? (lot?.productId ?? '') : (v.gln ?? '');
      const kind = Array.isArray(v.kind) ? v.kind[0] : v.kind;
      return api.post('/food/certificates', {
        kind, number: v.number, issuer: v.issuer, scope: v.scope, partnerId: v.partnerId, notes: v.notes, documentId,
        validFrom: v.validFrom ? dayjs(v.validFrom).format('YYYY-MM-DD') : undefined,
        validTo: v.validTo ? dayjs(v.validTo).format('YYYY-MM-DD') : undefined,
        links: [{ targetType: v.linkTo, target }],
      });
    },
    { invalidate: [certKey, ['/food/certificates']] },
  );
  const unlink = useAction((linkId: string) => api.del(`/food/certificates/links/${linkId}`), { invalidate: [certKey, ['/food/certificates']] });

  const openDocument = async (id: string, name: string) => {
    try {
      downloadBlob(await api.get<Blob>(`/documents/${id}`, undefined, { responseType: 'blob' }), name);
    } catch (e) {
      notifyError(e);
    }
  };

  return (
    <PageContainer title={`${t('field.lot')} ${tlc}`} subTitle={lot?.description} loading={lots.isLoading} onBack={() => history.back()}
      extra={<Link to={`/food/one-up-one-down?tlc=${encodeURIComponent(tlc)}`}><Button>{t('nav.oneUpOneDown')}</Button></Link>}>
      {!lots.isLoading && !lot ? (
        <Empty />
      ) : (
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          {lot && (
            <Card>
              <ProDescriptions<LotRow>
                column={{ xs: 1, md: 2, lg: 3 }}
                dataSource={lot}
                columns={[
                  { title: 'TLC', dataIndex: 'tlc', copyable: true },
                  { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <Tag>{r.status}</Tag> },
                  { title: t('field.gtin'), dataIndex: 'gtin' },
                  { title: t('field.quantity'), render: (_d, r) => `${r.quantity ?? '–'} ${r.uom ?? ''}` },
                  { title: t('field.gln'), dataIndex: 'locationGln' },
                  { title: 'TLC source', dataIndex: 'tlcSource' },
                  { title: 'Origin', dataIndex: 'origin' },
                  { title: 'Harvest date', dataIndex: 'harvestDate', valueType: 'date' },
                  { title: 'Pack date', dataIndex: 'packDate', valueType: 'date' },
                ]}
              />
            </Card>
          )}
          <Card
            title={t('foodCert.title')}
            extra={
              <ModalForm<CertForm>
                title={t('foodCert.add')}
                trigger={<Button type="primary" icon={<PlusOutlined />}>{t('foodCert.add')}</Button>}
                modalProps={{ destroyOnHidden: true }}
                initialValues={{ linkTo: 'lot' }}
                onFinish={add}
              >
                <ProFormSelect name="kind" label={t('field.kind')} mode="tags" options={KINDS.map((k) => ({ value: k, label: k }))} rules={[{ required: true }]}
                  fieldProps={{ maxCount: 1 }} />
                <ProFormText name="number" label={t('field.number')} />
                <ProFormText name="issuer" label={t('foodCert.issuer')} />
                <ProFormText name="scope" label={t('foodCert.scope')} />
                <ProFormDatePicker name="validFrom" label={t('field.validFrom')} />
                <ProFormDatePicker name="validTo" label={t('field.validTo')} />
                <ProFormSelect name="partnerId" label={t('field.partner')} options={partners} showSearch allowClear />
                <ProFormRadio.Group
                  name="linkTo"
                  label={t('foodCert.linkTo')}
                  options={[
                    { value: 'lot', label: t('foodCert.linkLot') },
                    { value: 'product', label: t('foodCert.linkProduct'), disabled: !lot?.productId },
                    { value: 'site', label: t('foodCert.linkSite') },
                  ]}
                />
                <ProFormDependency name={['linkTo']}>
                  {({ linkTo }) =>
                    linkTo === 'site' && (
                      <ProFormText name="gln" label={t('field.gln')} initialValue={lot?.tlcSource || lot?.locationGln}
                        rules={[{ required: true }, glnValidator(t('common.invalidGln'))]} fieldProps={{ maxLength: 13 }} />
                    )
                  }
                </ProFormDependency>
                <ProFormUploadButton name="document" label={t('foodCert.document')} max={1} extra={t('foodCert.documentHint')} fieldProps={{ beforeUpload: () => false }} />
                <ProFormTextArea name="notes" label={t('field.description')} />
              </ModalForm>
            }
          >
            <Table<FoodCertCoverage>
              size="small"
              rowKey={(c) => `${c.id}-${c.via}-${c.viaRef}`}
              loading={certs.isLoading}
              dataSource={certs.data?.certificates ?? []}
              locale={{ emptyText: t('foodCert.noneCover') }}
              pagination={false}
              scroll={{ x: 'max-content' }}
              columns={[
                { title: t('field.kind'), dataIndex: 'kind', render: (k: string) => <Tag color="blue">{k}</Tag> },
                { title: t('field.number'), dataIndex: 'number', render: (n: string) => (n ? <Typography.Text copyable>{n}</Typography.Text> : '–') },
                { title: t('foodCert.issuer'), dataIndex: 'issuer' },
                { title: t('foodCert.scope'), dataIndex: 'scope' },
                { title: t('field.validTo'), render: (_d, c) => <CertStatusTag c={c} /> },
                { title: t('foodCert.linkTo'), render: (_d, c) => <Tag>{t(`foodCert.via_${c.via}`)}{c.via !== 'lot' ? ` · ${c.via === 'supplier' ? c.supplier || c.viaRef : c.viaRef}` : ''}</Tag> },
                {
                  title: t('foodCert.document'),
                  render: (_d, c) => (c.documentId ? <a onClick={() => void openDocument(c.documentId!, `${c.kind}-${c.number || c.id}`)}>{t('action.download')}</a> : '–'),
                },
                {
                  title: t('common.actions'),
                  render: (_d, c) =>
                    c.linkId ? (
                      <Popconfirm title={t('foodCert.unlinkConfirm')} onConfirm={() => unlink.mutateAsync(c.linkId!)}>
                        <a>{t('action.remove')}</a>
                      </Popconfirm>
                    ) : (
                      '–'
                    ),
                },
              ]}
            />
            {certs.data?.certificates.some((c) => c.status === 'expired' || c.status === 'expiring') && (
              <Alert style={{ marginTop: 12 }} type="warning" showIcon title={`${t('foodCert.status_expired')} / ${t('foodCert.status_expiring')}`} />
            )}
          </Card>
          <Card title={t('foodCert.lineage')}>
            <Space wrap>
              {[...(trace.data?.sources ?? []), ...(trace.data?.derived ?? [])]
                .filter((x, i, all) => x !== tlc && all.indexOf(x) === i)
                .map((x) => (
                  <Link key={x} to={`/food/lots/${encodeURIComponent(x)}`}>
                    <Tag color={trace.data?.sources.includes(x) ? 'purple' : 'green'}>{x}</Tag>
                  </Link>
                ))}
            </Space>
          </Card>
          <Card title={t('foodCert.events')}>
            <Table
              size="small"
              rowKey="id"
              loading={trace.isLoading}
              dataSource={trace.data?.events ?? []}
              pagination={{ pageSize: 10 }}
              columns={[
                { title: t('field.eventTime'), dataIndex: 'eventTime', render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm') },
                { title: t('field.type'), dataIndex: 'type' },
                { title: 'TLC', dataIndex: 'tlc' },
                { title: t('field.quantity'), render: (_d, e) => `${e.quantity} ${e.uom}` },
              ]}
            />
          </Card>
        </Space>
      )}
    </PageContainer>
  );
}
