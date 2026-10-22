import { useTranslation } from 'react-i18next';
import { ORG_KINDS, ProFormSelect, ProFormText, type Organization } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';

export default function Organizations() {
  const { t } = useTranslation();
  const fields = (
    <>
      <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
      <ProFormSelect name="kind" label={t('field.kind')} rules={[{ required: true }]} options={ORG_KINDS.map((k) => ({ value: k, label: k }))} />
      <ProFormText name="companyPrefix" label="GS1 Company Prefix" rules={[{ pattern: /^\d{4,12}$/, message: '4–12 digits' }]} />
      <ProFormText name="country" label={t('field.country')} rules={[{ pattern: /^[A-Z]{2}$/, message: 'ISO 3166 alpha-2' }]} fieldProps={{ maxLength: 2 }} />
    </>
  );
  const pick = (v: Record<string, unknown>) => ({ name: v.name, kind: v.kind, companyPrefix: v.companyPrefix || null, country: v.country || null });
  return (
    <CrudPage<Organization & Record<string, unknown>>
      title={t('nav.organizations')}
      path="/orgs"
      columns={[
        { title: t('field.name'), dataIndex: 'name' },
        { title: t('field.kind'), dataIndex: 'kind' },
        { title: 'Company prefix', dataIndex: 'companyPrefix', copyable: true },
        { title: t('field.country'), dataIndex: 'country' },
        { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
      ]}
      create={{ fields, transform: pick }}
      edit={{ fields, transform: pick }}
    />
  );
}
