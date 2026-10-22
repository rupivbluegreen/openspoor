import { useTranslation } from 'react-i18next';
import { glnValidator, ProFormSelect, ProFormText, type Site } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { useOrgOptions } from '../../lib/options';

const SITE_KINDS = ['plant', 'warehouse', 'dc', 'pharmacy', 'hospital', 'farm', 'packhouse', 'store', 'office'];

export default function Sites() {
  const { t } = useTranslation();
  const orgs = useOrgOptions();
  const fields = (
    <>
      <ProFormSelect name="orgId" label={t('field.organization')} options={orgs} rules={[{ required: true }]} showSearch />
      <ProFormText name="gln" label="GLN" rules={[{ required: true }, glnValidator(t('common.invalidGln'))]} fieldProps={{ maxLength: 13 }} />
      <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
      <ProFormSelect name="kind" label={t('field.kind')} options={SITE_KINDS.map((k) => ({ value: k, label: k }))} />
      <ProFormText name="country" label={t('field.country')} fieldProps={{ maxLength: 2 }} />
      <ProFormText name={['address', 'street']} label="Street" />
      <ProFormText name={['address', 'city']} label="City" />
      <ProFormText name={['address', 'postalCode']} label="Postal code" />
    </>
  );
  const pick = (v: Record<string, unknown>) => ({ ...v, country: v.country || null, address: v.address ?? {} });
  return (
    <CrudPage<Site & Record<string, unknown>>
      title={t('nav.sites')}
      path="/sites"
      columns={[
        { title: t('field.name'), dataIndex: 'name' },
        { title: 'GLN', dataIndex: 'gln', copyable: true },
        { title: t('field.kind'), dataIndex: 'kind' },
        { title: t('field.organization'), dataIndex: 'orgId', render: (_d, r) => orgs.find((o) => o.value === r.orgId)?.label ?? r.orgId },
        { title: t('field.country'), dataIndex: 'country' },
        { title: 'City', dataIndex: ['address', 'city'] },
      ]}
      create={{ fields, transform: pick }}
      edit={{ fields, transform: pick }}
    />
  );
}
