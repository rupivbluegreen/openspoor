import { useState } from 'react';
import { Alert, Button, Modal, Table, Tag, Upload } from 'antd';
import { UploadOutlined } from '@ant-design/icons';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, gtinValidator, notifyError, ProFormDigit, ProFormSelect, ProFormText, ProFormTextArea, type Product } from '@openspoor/ui/pro';
import { useQueryClient } from '@tanstack/react-query';
import { CrudPage } from '../../components/CrudPage';
import { MARKETS } from '../../lib/options';

type Row = Product & Record<string, unknown>;
interface ImportResult {
  created?: number;
  updated?: number;
  errors?: { line: number; message: string }[];
}

export function ProductFields() {
  const { t } = useTranslation();
  return (
    <>
      <ProFormText name="gtin" label="GTIN-14" rules={[{ required: true }, gtinValidator(t('common.invalidGtin'))]} fieldProps={{ maxLength: 14 }} />
      <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
      <ProFormTextArea name="description" label={t('field.description')} fieldProps={{ rows: 2 }} />
      <ProFormSelect name="vertical" label="Vertical" initialValue="pharma" options={['pharma', 'food', 'device'].map((v) => ({ value: v, label: v }))} />
      <ProFormText name="ndc" label="NDC" tooltip="US National Drug Code" />
      <ProFormDigit name="companyPrefixLength" label="Company prefix length" min={6} max={12} fieldProps={{ precision: 0 }} />
      <ProFormSelect name="targetMarkets" label="Target markets" mode="tags" options={MARKETS} />
      <ProFormText name="dosageForm" label="Dosage form" />
      <ProFormText name="strength" label="Strength" />
      <ProFormText name="packSize" label="Pack size" />
    </>
  );
}

function ImportButton() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [result, setResult] = useState<ImportResult | null>(null);
  const [busy, setBusy] = useState(false);
  return (
    <>
      <Upload
        accept=".csv,text/csv"
        showUploadList={false}
        beforeUpload={async (file) => {
          setBusy(true);
          try {
            const res = await api.request<ImportResult>('POST', '/products/import', { body: await file.text(), headers: { 'Content-Type': 'text/csv' } });
            setResult(res ?? {});
            await qc.invalidateQueries({ queryKey: ['/products'] });
          } catch (e) {
            notifyError(e);
          } finally {
            setBusy(false);
          }
          return false;
        }}
      >
        <Button icon={<UploadOutlined />} loading={busy}>
          {t('action.import')} CSV
        </Button>
      </Upload>
      <Modal open={!!result} title={`${t('action.import')} CSV`} onCancel={() => setResult(null)} footer={null} width={640}>
        <Alert
          type={result?.errors?.length ? 'warning' : 'success'}
          showIcon
          title={`created: ${result?.created ?? 0} · updated: ${result?.updated ?? 0} · errors: ${result?.errors?.length ?? 0}`}
          style={{ marginBottom: 12 }}
        />
        {!!result?.errors?.length && (
          <Table size="small" rowKey={(r) => `${r.line}-${r.message}`} dataSource={result.errors} pagination={false} columns={[{ title: 'Line', dataIndex: 'line', width: 80 }, { title: 'Message', dataIndex: 'message' }]} />
        )}
      </Modal>
    </>
  );
}

const CSV_HEADER = 'gtin,name,ndc,company_prefix_length,target_markets,dosage_form,strength,pack_size';

export default function Products() {
  const { t } = useTranslation();
  const toBody = (v: Record<string, unknown>) => ({ ...v, packSize: v.packSize === undefined ? undefined : String(v.packSize) });
  return (
    <CrudPage<Row>
      title={t('nav.products')}
      subTitle={<span>CSV header: <code>{CSV_HEADER}</code></span>}
      path="/products"
      table={{ search: { labelWidth: 'auto' } }}
      columns={[
        { title: 'GTIN', dataIndex: 'gtin', copyable: true, render: (_d, r) => <Link to={`/settings/products/${r.id}`}>{r.gtin}</Link> },
        { title: t('field.name'), dataIndex: 'name' },
        { title: 'NDC', dataIndex: 'ndc', search: false },
        { title: 'Vertical', dataIndex: 'vertical', search: false },
        { title: 'Markets', dataIndex: 'targetMarkets', search: false, render: (_d, r) => r.targetMarkets?.map((m) => <Tag key={m}>{m}</Tag>) },
        { title: 'Dosage form', dataIndex: 'dosageForm', search: false },
        { title: 'Strength', dataIndex: 'strength', search: false },
      ]}
      toolbar={[<ImportButton key="import" />]}
      create={{ width: 640, fields: <ProductFields />, transform: toBody }}
      edit={{ width: 640, fields: <ProductFields />, transform: toBody }}
    />
  );
}
