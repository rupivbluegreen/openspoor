import { Tag } from 'antd';
import { useTranslation } from 'react-i18next';
import { CodeView, prettyJson, ProForm, ProFormList, ProFormSelect, ProFormText, type Cte } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { CTE_TYPES } from '../../lib/options';

export default function Ctes() {
  const { t } = useTranslation();
  return (
    <CrudPage<Cte & Record<string, unknown>>
      title={t('nav.ctes')}
      subTitle="FSMA 204 critical tracking events + key data elements"
      path="/food/cte"
      rowKey={'id'}
      table={{
        rowKey: (r) => String(r.id ?? `${r.type}-${r.tlc}-${r.createdAt}`),
        expandable: { expandedRowRender: (r) => <CodeView code={prettyJson(r.kdes ?? {})} maxHeight={260} /> },
      }}
      columns={[
        { title: t('field.type'), dataIndex: 'type', render: (_d, r) => <Tag>{r.type}</Tag> },
        { title: 'TLC', dataIndex: 'tlc', copyable: true },
        { title: 'KDEs', render: (_d, r) => Object.keys(r.kdes ?? {}).length },
        { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
      ]}
      create={{
        width: 720,
        initialValues: { kdes: [{ key: 'location', value: '' }, { key: 'date', value: '' }] },
        fields: (
          <>
            <ProFormSelect name="type" label={t('field.type')} options={CTE_TYPES} rules={[{ required: true }]} />
            <ProFormText name="tlc" label="TLC" rules={[{ required: true }]} />
            <ProFormList name="kdes" label="Key data elements" creatorButtonProps={{ creatorButtonText: t('action.add') }}>
              <ProForm.Group>
                <ProFormText name="key" label="KDE" width="sm" rules={[{ required: true }]} />
                <ProFormText name="value" label="Value" width="md" />
              </ProForm.Group>
            </ProFormList>
          </>
        ),
        transform: (v) => ({
          type: v.type,
          tlc: v.tlc,
          kdes: Object.fromEntries(((v.kdes as { key: string; value: string }[]) ?? []).filter((x) => x?.key).map((x) => [x.key, x.value])),
        }),
      }}
    />
  );
}
