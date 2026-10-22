import { Card, Col, Row } from 'antd';
import { useTranslation } from 'react-i18next';
import { api, PageContainer, ProForm, ProFormDigit, ProFormList, ProFormSelect, ProFormText, QueryTable, type Cte } from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import { useProductOptions } from '../../lib/options';

interface Form {
  inputs: { tlc: string; quantity: number }[];
  output: { tlc: string; productId: string; quantity: number };
}

export default function Transformations() {
  const { t } = useTranslation();
  const products = useProductOptions();
  const submit = useSubmit((v: Form) => api.post('/food/transformations', v), { invalidate: [['/food/cte'], ['/food/lots']] });
  return (
    <PageContainer title={t('nav.transformations')} subTitle="Inputs → new traceability lot">
      <Row gutter={[16, 16]}>
        <Col xs={24} xl={10}>
          <Card title={t('action.create')}>
            <ProForm<Form> initialValues={{ inputs: [{}] }} onFinish={submit} submitter={{ searchConfig: { submitText: t('action.submit') } }}>
              <ProFormList name="inputs" label="Inputs" min={1} creatorButtonProps={{ creatorButtonText: t('action.add') }}>
                <ProForm.Group>
                  <ProFormText name="tlc" label="TLC" width="sm" rules={[{ required: true }]} />
                  <ProFormDigit name="quantity" label={t('field.quantity')} width="xs" min={0} rules={[{ required: true }]} />
                </ProForm.Group>
              </ProFormList>
              <ProForm.Group title="Output">
                <ProFormText name={['output', 'tlc']} label="TLC" width="sm" rules={[{ required: true }]} />
                <ProFormSelect name={['output', 'productId']} label={t('field.product')} width="md" options={products} rules={[{ required: true }]} />
                <ProFormDigit name={['output', 'quantity']} label={t('field.quantity')} width="xs" min={0} rules={[{ required: true }]} />
              </ProForm.Group>
            </ProForm>
          </Card>
        </Col>
        <Col xs={24} xl={14}>
          <QueryTable<Cte & Record<string, unknown>>
            headerTitle="Transformation CTEs"
            rowKey={(r) => String(r.id ?? `${r.tlc}-${r.createdAt}`)}
            queryKey={['/food/cte', 'transformation']}
            fetcher={async () => {
              const page = await api.list<Cte & Record<string, unknown>>('/food/cte');
              const items = page.items.filter((c) => c.type === 'transformation');
              return { items, total: items.length };
            }}
            columns={[
              { title: 'TLC', dataIndex: 'tlc', copyable: true },
              { title: 'KDEs', render: (_d, r) => JSON.stringify(r.kdes ?? {}), ellipsis: true },
              { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
            ]}
          />
        </Col>
      </Row>
    </PageContainer>
  );
}
