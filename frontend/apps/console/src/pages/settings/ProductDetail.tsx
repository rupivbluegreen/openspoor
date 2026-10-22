import { useState } from 'react';
import { Alert, Card, Empty, Select, Space, Tag } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  api, ApiError, gtinValidator, PageContainer, ProDescriptions, ProForm, ProFormDigit, ProFormList, ProFormSelect, ProFormText,
  type PackagingLevel, type PackagingLevelName, type Product,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import { MARKETS } from '../../lib/options';

const LEVELS: PackagingLevelName[] = ['unit', 'bundle', 'case', 'pallet', 'shipment'];
const DEFAULT_LEVELS: PackagingLevel[] = [
  { level: 'unit', quantity: 1 },
  { level: 'case', quantity: 50 },
  { level: 'pallet', quantity: 40 },
];

function PackagingEditor({ product, market }: { product: Product; market: string }) {
  const { t } = useTranslation();
  const key = ['/products', product.id, 'packaging', market];
  const q = useQuery({
    queryKey: key,
    queryFn: async () => {
      try {
        return await api.get<{ levels?: PackagingLevel[] }>(`/products/${product.id}/packaging/${market}`);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return { levels: [] };
        throw e;
      }
    },
  });
  const save = useSubmit((v: { levels: PackagingLevel[] }) => api.put(`/products/${product.id}/packaging/${market}`, { levels: v.levels }), {
    invalidate: [key],
    success: t('common.saved'),
  });
  if (q.isLoading) return <Card loading />;
  const levels = q.data?.levels?.length ? q.data.levels : DEFAULT_LEVELS.map((l) => (l.level === 'unit' ? { ...l, gtin: product.gtin } : l));
  return (
    <ProForm<{ levels: PackagingLevel[] }>
      key={`${market}-${q.dataUpdatedAt}`}
      initialValues={{ levels }}
      onFinish={save}
      submitter={{ searchConfig: { submitText: t('action.save') } }}
    >
      <Alert type="info" showIcon style={{ marginBottom: 16 }} title="Quantity = number of children of the previous level contained in this level (unit = 1)." />
      <ProFormList name="levels" creatorButtonProps={{ creatorButtonText: t('action.add') }} copyIconProps={false}>
        <ProForm.Group>
          <ProFormSelect name="level" label="Level" width="sm" rules={[{ required: true }]} options={LEVELS.map((l) => ({ value: l, label: l }))} />
          <ProFormText name="gtin" label="GTIN" width="md" rules={[gtinValidator(t('common.invalidGtin'))]} fieldProps={{ maxLength: 14 }} />
          <ProFormDigit name="quantity" label={t('field.quantity')} width="xs" min={1} rules={[{ required: true }]} fieldProps={{ precision: 0 }} />
        </ProForm.Group>
      </ProFormList>
    </ProForm>
  );
}

export default function ProductDetail() {
  const { id = '' } = useParams();
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ['/products', id], queryFn: () => api.get<Product>(`/products/${id}`) });
  const markets = q.data?.targetMarkets?.length ? q.data.targetMarkets : ['US'];
  const [market, setMarket] = useState<string | undefined>();
  const current = market ?? markets[0];

  return (
    <PageContainer title={q.data?.name ?? t('field.product')} loading={q.isLoading} onBack={() => history.back()}>
      {q.data ? (
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <Card>
            <ProDescriptions<Product>
              column={{ xs: 1, md: 2, lg: 3 }}
              dataSource={q.data}
              columns={[
                { title: 'GTIN', dataIndex: 'gtin', copyable: true },
                { title: 'NDC', dataIndex: 'ndc' },
                { title: 'Vertical', dataIndex: 'vertical' },
                { title: 'Dosage form', dataIndex: 'dosageForm' },
                { title: 'Strength', dataIndex: 'strength' },
                { title: 'Pack size', dataIndex: 'packSize' },
                { title: 'Markets', dataIndex: 'targetMarkets', render: (_d, r) => r.targetMarkets?.map((m) => <Tag key={m}>{m}</Tag>) },
                { title: 'Market codes', dataIndex: 'marketCodes', render: (_d, r) => Object.entries(r.marketCodes ?? {}).map(([k, v]) => <Tag key={k}>{k}: {v}</Tag>) },
                { title: t('field.description'), dataIndex: 'description', span: 3 },
              ]}
            />
          </Card>
          <Card
            title="Packaging hierarchy"
            extra={
              <Select
                value={current}
                style={{ minWidth: 120 }}
                onChange={setMarket}
                options={[...new Set([...markets, ...MARKETS.map((m) => m.value)])].map((m) => ({ value: m, label: m }))}
              />
            }
          >
            <PackagingEditor product={q.data} market={current} />
          </Card>
        </Space>
      ) : (
        !q.isLoading && <Empty />
      )}
    </PageContainer>
  );
}
