import { Tag } from 'antd';
import { useTranslation } from 'react-i18next';
import { ProFormDigit, ProFormSelect, ProFormText, StatusTag, type Dds } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { usePlotOptions } from '../../lib/options';

const COMMODITIES = ['cattle', 'cocoa', 'coffee', 'oil_palm', 'rubber', 'soya', 'wood'];

export default function Eudr() {
  const { t } = useTranslation();
  const plots = usePlotOptions();
  return (
    <CrudPage<Dds & Record<string, unknown>>
      title={t('nav.eudr')}
      subTitle="Due diligence statements (Regulation (EU) 2023/1115)"
      path="/food/eudr/dds"
      table={{ rowKey: (r) => String(r.id ?? r.reference) }}
      columns={[
        { title: 'Reference', dataIndex: 'reference', copyable: true },
        { title: t('field.commodity'), dataIndex: 'commodity' },
        { title: 'Plots', dataIndex: 'plotIds', render: (_d, r) => r.plotIds?.map((p) => <Tag key={p}>{plots.find((x) => x.value === p)?.label ?? p}</Tag>) },
        { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
        { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
      ]}
      create={{
        fields: (
          <>
            <ProFormSelect name="commodity" label={t('field.commodity')} options={COMMODITIES} rules={[{ required: true }]} />
            <ProFormText name="hsCode" label="HS code" />
            <ProFormDigit name="quantity" label={`${t('field.quantity')} (kg)`} min={0} />
            <ProFormSelect name="plotIds" label="Plots" mode="multiple" options={plots} rules={[{ required: true }]} />
            <ProFormText name="countryOfProduction" label={t('field.country')} fieldProps={{ maxLength: 2 }} />
          </>
        ),
      }}
    />
  );
}
