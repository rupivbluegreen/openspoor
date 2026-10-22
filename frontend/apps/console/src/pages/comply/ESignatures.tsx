import { useState } from 'react';
import { Button } from 'antd';
import { SafetyCertificateOutlined } from '@ant-design/icons';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, ESignatureModal, ModalForm, PageContainer, ProFormText, QueryTable, type ESignature } from '@openspoor/ui/pro';

type Row = ESignature & Record<string, unknown>;

export default function ESignatures() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [target, setTarget] = useState<{ entity: string; entityId: string } | null>(null);
  return (
    <PageContainer title={t('nav.esignatures')} subTitle="21 CFR Part 11 / EU Annex 11">
      <QueryTable<Row>
        rowKey="id"
        queryKey={['/esignatures']}
        search={{ labelWidth: 'auto' }}
        fetcher={({ current: _c, pageSize: _p, ...q }) => api.list<Row>('/esignatures', q as Record<string, string>)}
        columns={[
          { title: t('field.at'), dataIndex: 'signedAt', valueType: 'dateTime', search: false },
          { title: t('field.email'), dataIndex: 'email', search: false },
          { title: t('field.meaning'), dataIndex: 'meaning', search: false },
          { title: t('field.reason'), dataIndex: 'reason', search: false, ellipsis: true },
          { title: t('field.entity'), dataIndex: 'entity' },
          { title: t('field.entityId'), dataIndex: 'entityId', copyable: true },
          { title: 'Audit seq', dataIndex: 'auditSeq', search: false },
        ]}
        toolBarRender={() => [
          <ModalForm<{ entity: string; entityId: string }>
            key="sign"
            title={t('esig.title')}
            trigger={<Button type="primary" icon={<SafetyCertificateOutlined />}>{t('action.sign')}</Button>}
            modalProps={{ destroyOnHidden: true }}
            onFinish={async (v) => {
              setTarget(v);
              return true;
            }}
          >
            <ProFormText name="entity" label={t('field.entity')} rules={[{ required: true }]} placeholder="report, batch_record, recall…" />
            <ProFormText name="entityId" label={t('field.entityId')} rules={[{ required: true }]} />
          </ModalForm>,
        ]}
      />
      {target && (
        <ESignatureModal
          open
          entity={target.entity}
          entityId={target.entityId}
          onOpenChange={(o) => !o && setTarget(null)}
          onSigned={() => {
            setTarget(null);
            void qc.invalidateQueries({ queryKey: ['/esignatures'] });
          }}
        />
      )}
    </PageContainer>
  );
}
