import dayjs from 'dayjs';
import { Tag } from 'antd';
import { useTranslation } from 'react-i18next';
import { ProFormDatePicker, ProFormDigit, ProFormSelect, ProFormText, type FoodLot } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { UOMS, usePlotOptions, useProductOptions, useSiteOptions } from '../../lib/options';

export default function Lots() {
  const { t } = useTranslation();
  const products = useProductOptions();
  const sites = useSiteOptions();
  const plots = usePlotOptions();
  return (
    <CrudPage<FoodLot & Record<string, unknown>>
      title={t('nav.lots')}
      subTitle="Traceability lot codes (TLC)"
      path="/food/lots"
      rowKey="tlc"
      columns={[
        { title: 'TLC', dataIndex: 'tlc', copyable: true },
        { title: t('field.product'), dataIndex: 'productId', render: (_d, r) => products.find((p) => p.value === r.productId)?.label ?? r.productId ?? '–' },
        { title: t('field.quantity'), render: (_d, r) => `${r.quantity ?? '–'} ${r.uom ?? ''}` },
        { title: t('field.site'), dataIndex: 'siteId', render: (_d, r) => sites.find((s) => s.value === r.siteId)?.label ?? r.siteId ?? '–' },
        { title: 'Harvest date', dataIndex: 'harvestDate', valueType: 'date' },
        { title: 'Plots', dataIndex: 'plotIds', render: (_d, r) => r.plotIds?.map((p) => <Tag key={p}>{plots.find((x) => x.value === p)?.label ?? p}</Tag>) },
      ]}
      create={{
        fields: (
          <>
            <ProFormText name="tlc" label="Traceability lot code" rules={[{ required: true }]} />
            <ProFormSelect name="productId" label={t('field.product')} options={products} showSearch rules={[{ required: true }]} />
            <ProFormDigit name="quantity" label={t('field.quantity')} min={0} rules={[{ required: true }]} />
            <ProFormSelect name="uom" label="UoM" options={UOMS} initialValue="kg" />
            <ProFormSelect name="siteId" label={t('field.site')} options={sites} showSearch />
            <ProFormDatePicker name="harvestDate" label="Harvest date" />
            <ProFormSelect name="plotIds" label="Plots" mode="multiple" options={plots} />
          </>
        ),
        transform: (v) => ({ ...v, harvestDate: v.harvestDate ? dayjs(v.harvestDate as string).format('YYYY-MM-DD') : undefined }),
      }}
    />
  );
}
