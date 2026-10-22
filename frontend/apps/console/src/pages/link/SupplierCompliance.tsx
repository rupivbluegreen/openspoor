import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import { ProFormDatePicker, ProFormSelect, ProFormText, type Certificate } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { usePartnerOptions } from '../../lib/options';
import { expiryTag } from '../settings/Licenses';

const KINDS = ['GMP', 'GDP', 'ISO 9001', 'ISO 22000', 'FSSC 22000', 'BRCGS', 'GlobalG.A.P.', 'Organic', 'Halal', 'Kosher'];

export default function SupplierCompliance() {
  const { t } = useTranslation();
  const partners = usePartnerOptions();
  const iso = (v?: unknown) => (v ? dayjs(v as string).toISOString() : undefined);
  return (
    <CrudPage<Certificate & Record<string, unknown>>
      title={t('nav.supplierCompliance')}
      path="/suppliers/certificates"
      rowKey="id"
      columns={[
        { title: 'Supplier', render: (_d, r) => r.supplier ?? partners.find((p) => p.value === (r.partnerId ?? r.supplierId))?.label ?? r.partnerId ?? r.supplierId ?? '–' },
        { title: t('field.kind'), render: (_d, r) => r.kind ?? r.type ?? '–' },
        { title: t('field.number'), dataIndex: 'number', copyable: true },
        { title: t('field.validTo'), render: (_d, r) => expiryTag((r.validTo ?? r.expiresAt) as string | undefined) },
      ]}
      create={{
        fields: (
          <>
            <ProFormSelect name="partnerId" label="Supplier" options={partners} rules={[{ required: true }]} showSearch />
            <ProFormSelect name="kind" label={t('field.kind')} options={KINDS} rules={[{ required: true }]} />
            <ProFormText name="number" label={t('field.number')} />
            <ProFormText name="issuer" label={t('field.authority')} />
            <ProFormDatePicker name="validFrom" label={t('field.validFrom')} />
            <ProFormDatePicker name="validTo" label={t('field.validTo')} rules={[{ required: true }]} />
          </>
        ),
        transform: (v) => ({ ...v, validFrom: iso(v.validFrom), validTo: iso(v.validTo) }),
      }}
    />
  );
}
