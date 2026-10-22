import { Tag } from 'antd';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import { ProFormDatePicker, ProFormSelect, ProFormText, type License } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { useOrgOptions, useSiteOptions } from '../../lib/options';

export function expiryTag(validTo?: string) {
  if (!validTo) return '–';
  const days = dayjs(validTo).diff(dayjs(), 'day');
  const color = days < 0 ? 'error' : days < 60 ? 'warning' : 'success';
  return <Tag color={color}>{dayjs(validTo).format('YYYY-MM-DD')} {days < 0 ? '(expired)' : `(${days}d)`}</Tag>;
}

export default function Licenses() {
  const { t } = useTranslation();
  const orgs = useOrgOptions();
  const sites = useSiteOptions();
  return (
    <CrudPage<License & Record<string, unknown>>
      title={t('nav.licenses')}
      path="/licenses"
      columns={[
        { title: t('field.kind'), dataIndex: 'kind' },
        { title: t('field.number'), dataIndex: 'number', copyable: true },
        { title: t('field.authority'), dataIndex: 'authority' },
        { title: t('field.organization'), dataIndex: 'orgId', render: (_d, r) => orgs.find((o) => o.value === r.orgId)?.label ?? r.orgId },
        { title: t('field.country'), dataIndex: 'country' },
        { title: t('field.validFrom'), dataIndex: 'validFrom', valueType: 'date' },
        { title: t('field.validTo'), dataIndex: 'validTo', render: (_d, r) => expiryTag(r.validTo) },
      ]}
      create={{
        transform: (v) => ({
          ...v,
          validFrom: v.validFrom ? dayjs(v.validFrom as string).toISOString() : undefined,
          validTo: v.validTo ? dayjs(v.validTo as string).toISOString() : undefined,
        }),
        fields: (
          <>
            <ProFormSelect name="orgId" label={t('field.organization')} options={orgs} rules={[{ required: true }]} />
            <ProFormSelect name="siteId" label={t('field.site')} options={sites} allowClear />
            <ProFormSelect
              name="kind"
              label={t('field.kind')}
              rules={[{ required: true }]}
              fieldProps={{ mode: 'tags', maxCount: 1 }}
              options={['state_wholesale', '3pl', 'manufacturer', 'dea', 'gdp', 'gmp', 'sfda', 'pharmacy'].map((k) => ({ value: k, label: k }))}
              transform={(v: unknown) => ({ kind: Array.isArray(v) ? v[0] : v })}
            />
            <ProFormText name="number" label={t('field.number')} rules={[{ required: true }]} />
            <ProFormText name="authority" label={t('field.authority')} rules={[{ required: true }]} />
            <ProFormText name="country" label={t('field.country')} fieldProps={{ maxLength: 2 }} />
            <ProFormDatePicker name="validFrom" label={t('field.validFrom')} />
            <ProFormDatePicker name="validTo" label={t('field.validTo')} />
          </>
        ),
      }}
    />
  );
}
