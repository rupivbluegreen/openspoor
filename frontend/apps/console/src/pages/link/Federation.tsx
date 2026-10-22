import { useTranslation } from 'react-i18next';
import { ProFormText, ProFormTextArea, StatusTag, type FederationPeer } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';

export default function Federation() {
  const { t } = useTranslation();
  return (
    <CrudPage<FederationPeer & Record<string, unknown>>
      title={t('nav.federation')}
      subTitle="Node-to-node signed messaging"
      path="/federation/peers"
      rowKey="id"
      columns={[
        { title: t('field.name'), dataIndex: 'name' },
        { title: 'URL', dataIndex: 'url', copyable: true },
        { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
        { title: 'Public key', dataIndex: 'publicKey', ellipsis: true, copyable: true },
      ]}
      create={{
        fields: (
          <>
            <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
            <ProFormText name="url" label="URL" rules={[{ required: true }, { type: 'url' }]} placeholder="https://node.partner.example" />
            <ProFormTextArea name="publicKey" label="Public key" fieldProps={{ rows: 4, style: { fontFamily: 'monospace' } }} />
          </>
        ),
      }}
    />
  );
}
