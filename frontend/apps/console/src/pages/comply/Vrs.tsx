import { useState } from 'react';
import { Button, Modal, Result, Tabs, Tag } from 'antd';
import { CheckCircleOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import {
  api, glnValidator, gtinValidator, ModalForm, PageContainer, ProFormSelect, ProFormText, QueryTable, type VrsDirectoryEntry, type VrsRequest,
} from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { useSubmit } from '../../lib/hooks';

const CONTEXTS = ['dscsaSaleableReturn', 'dscsaSuspect', 'dscsaIllegitimate', 'dscsaDispense'];

function Requests() {
  const { t } = useTranslation();
  const [result, setResult] = useState<VrsRequest | null>(null);
  const verify = useSubmit((v: Record<string, unknown>) => api.post<VrsRequest>('/vrs/requests', v), { invalidate: [['/vrs/requests']], success: false, onSuccess: (r) => setResult(r) });
  return (
    <>
      <QueryTable<VrsRequest & Record<string, unknown>>
        rowKey={(r) => String(r.id ?? `${r.gtin}-${r.serial}-${r.createdAt}`)}
        queryKey={['/vrs/requests']}
        fetcher={() => api.list('/vrs/requests')}
        columns={[
          { title: 'GTIN', dataIndex: 'gtin', copyable: true },
          { title: t('field.serial'), dataIndex: 'serial', copyable: true },
          { title: t('field.lot'), dataIndex: 'lot' },
          { title: t('field.expiry'), dataIndex: 'expiry' },
          { title: 'Context', dataIndex: 'context' },
          { title: t('common.result'), dataIndex: 'verified', render: (_d, r) => (r.verified === undefined ? '–' : r.verified ? <Tag color="success">{t('verify.verified')}</Tag> : <Tag color="error">{t('verify.notFound')}</Tag>) },
          { title: 'Responder', dataIndex: 'responder', ellipsis: true },
          { title: 'Latency (ms)', dataIndex: 'latencyMs', valueType: 'digit' },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
        ]}
        toolBarRender={() => [
          <ModalForm<Record<string, unknown>>
            key="new"
            title="Verification request"
            trigger={<Button type="primary" icon={<CheckCircleOutlined />}>{t('action.verify')}</Button>}
            modalProps={{ destroyOnHidden: true }}
            initialValues={{ context: 'dscsaSaleableReturn' }}
            onFinish={verify}
          >
            <ProFormText name="gtin" label="GTIN" rules={[{ required: true }, gtinValidator(t('common.invalidGtin'))]} />
            <ProFormText name="serial" label={t('field.serial')} rules={[{ required: true }]} />
            <ProFormText name="lot" label={t('field.lot')} rules={[{ required: true }]} />
            <ProFormText name="expiry" label={`${t('field.expiry')} (YYMMDD)`} rules={[{ required: true, pattern: /^\d{6}$/ }]} />
            <ProFormText name="requestorGln" label="Requestor GLN" rules={[glnValidator(t('common.invalidGln'))]} />
            <ProFormSelect name="context" label="Context" options={CONTEXTS} />
          </ModalForm>,
        ]}
      />
      <Modal open={!!result} onCancel={() => setResult(null)} footer={null}>
        <Result
          status={result?.verified ? 'success' : 'error'}
          title={result?.verified ? t('verify.verified') : t('verify.notFound')}
          subTitle={`${result?.responder ?? ''}${result?.latencyMs !== undefined ? ` · ${result.latencyMs} ms` : ''}`}
        />
      </Modal>
    </>
  );
}

export default function Vrs() {
  const { t } = useTranslation();
  return (
    <PageContainer title={t('nav.vrs')} subTitle="GS1 Lightweight Messaging Standard">
      <Tabs
        items={[
          { key: 'req', label: 'Requests', children: <Requests /> },
          {
            key: 'dir',
            label: 'Lookup directory',
            children: (
              <CrudPage<VrsDirectoryEntry & Record<string, unknown>>
                wrap={false}
                title=""
                path="/vrs/directory"
                rowKey="gtinPrefix"
                columns={[
                  { title: 'GTIN prefix', dataIndex: 'gtinPrefix' },
                  { title: 'Responder URL', dataIndex: 'responderUrl', copyable: true },
                  { title: 'Auth', dataIndex: 'auth' },
                ]}
                create={{
                  fields: (
                    <>
                      <ProFormText name="gtinPrefix" label="GTIN prefix" rules={[{ required: true, pattern: /^\d{4,14}$/ }]} />
                      <ProFormText name="responderUrl" label="Responder URL" rules={[{ required: true }, { type: 'url' }]} />
                      <ProFormSelect name="auth" label="Auth" options={['none', 'apikey', 'oauth2', 'mtls']} />
                    </>
                  ),
                }}
              />
            ),
          },
        ]}
      />
    </PageContainer>
  );
}
