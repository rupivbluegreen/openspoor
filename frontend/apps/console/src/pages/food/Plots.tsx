import { Flex } from 'antd';
import { useTranslation } from 'react-i18next';
import { CodeView, prettyJson, ProFormSelect, ProFormText, ProFormTextArea, type Plot } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { GeoPreview } from '../../components/GeoPreview';

const COMMODITIES = ['cattle', 'cocoa', 'coffee', 'oil_palm', 'rubber', 'soya', 'wood', 'produce', 'seafood'];
const SAMPLE = JSON.stringify(
  { type: 'Polygon', coordinates: [[[5.2913, 52.1326], [5.2961, 52.1331], [5.2972, 52.1302], [5.2925, 52.1294], [5.2913, 52.1326]]] },
);

export default function Plots() {
  const { t } = useTranslation();
  return (
    <CrudPage<Plot & Record<string, unknown>>
      title={t('nav.plots')}
      subTitle="Farm / plot registry (EUDR geolocation)"
      path="/food/plots"
      table={{
        rowKey: (r) => String(r.id ?? r.name),
        expandable: {
          expandedRowRender: (r) => (
            <Flex gap={16} wrap>
              <GeoPreview geojson={r.geojson} size={260} />
              <div style={{ flex: 1, minWidth: 280 }}>
                <CodeView code={prettyJson(r.geojson ?? {})} filename={`${r.name}.geojson`} mime="application/geo+json" maxHeight={260} />
              </div>
            </Flex>
          ),
        },
      }}
      columns={[
        { title: t('action.preview'), width: 80, render: (_d, r) => <GeoPreview geojson={r.geojson} size={56} /> },
        { title: t('field.name'), dataIndex: 'name' },
        { title: t('field.country'), dataIndex: 'country' },
        { title: t('field.commodity'), dataIndex: 'commodity' },
      ]}
      create={{
        width: 720,
        initialValues: { geojson: SAMPLE },
        fields: (
          <>
            <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
            <ProFormText name="country" label={t('field.country')} fieldProps={{ maxLength: 2 }} />
            <ProFormSelect name="commodity" label={t('field.commodity')} options={COMMODITIES} />
            <ProFormTextArea
              name="geojson"
              label="GeoJSON (WGS84)"
              rules={[{ required: true }, { validator: async (_r, v) => { if (v) JSON.parse(v as string); } }]}
              fieldProps={{ rows: 8, style: { fontFamily: 'monospace' } }}
            />
          </>
        ),
        transform: (v) => ({ ...v, geojson: JSON.parse(v.geojson as string) }),
      }}
    />
  );
}
