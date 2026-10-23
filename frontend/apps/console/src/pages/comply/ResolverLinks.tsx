import { Alert, Popconfirm, Tag } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, notifySuccess, ProFormSelect, ProFormSwitch, ProFormText, StatusTag, type ResolverLinkRow, type ResolverLinkType } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { useSession } from '../../lib/session';

const AUDIENCES = ['consumer', 'authority', 'trading_partner'] as const;

/** Typed links a GS1 Digital Link resolves to, with per-audience access. */
export default function ResolverLinks() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canWrite = hasRole('Compliance');
  const types = useQuery({ queryKey: ['/resolver/link-types'], queryFn: () => api.list<ResolverLinkType>('/resolver/link-types') });
  const typeHint = [...(types.data?.items ?? []).map((x) => `${x.id} – ${x.title}`), 'custom:<name>'].join('\n');
  const fields = (
    <>
      <ProFormText name="gtin" label={t('field.gtin')} rules={[{ required: true }]} />
      <ProFormText name="lot" label={t('resolver.lot')} />
      <ProFormText name="linkType" label={t('resolver.linkType')} tooltip={<span style={{ whiteSpace: 'pre-line' }}>{typeHint}</span>} placeholder="gs1:pip" rules={[{ required: true }]} />
      <ProFormSelect name="audiences" label={t('resolver.audiences')} mode="multiple" options={AUDIENCES.map((a) => ({ value: a, label: t(`resolver.${a}`) }))} initialValue={['consumer']} />
      <ProFormText name="href" label={t('resolver.href')} rules={[{ required: true }]} />
      <ProFormText name="mimeType" label={t('resolver.mimeType')} initialValue="text/html" />
      <ProFormText name="lang" label={t('resolver.lang')} />
      <ProFormText name="title" label={t('resolver.title')} />
      <ProFormSwitch name="isDefault" label={t('resolver.isDefault')} />
      <ProFormSwitch name="active" label={t('resolver.active')} initialValue />
    </>
  );
  return (
    <CrudPage<ResolverLinkRow & Record<string, unknown>>
      title={t('nav.resolverLinks')}
      subTitle={t('resolver.subtitle')}
      path="/resolver/links"
      header={<Alert type="info" showIcon style={{ marginBottom: 16 }} message={t('resolver.specNote')} />}
      columns={[
        { title: t('field.gtin'), dataIndex: 'gtin' },
        { title: t('field.lot'), dataIndex: 'lot', render: (_d, r) => r.lot || '–' },
        { title: t('resolver.linkType'), dataIndex: 'linkType' },
        { title: t('resolver.audiences'), render: (_d, r) => r.audiences.map((a) => <Tag key={a}>{t(`resolver.${a}`)}</Tag>) },
        { title: t('resolver.lang'), dataIndex: 'lang', render: (_d, r) => r.lang || '–' },
        { title: t('resolver.href'), dataIndex: 'href', ellipsis: true },
        { title: t('resolver.isDefault'), dataIndex: 'isDefault', render: (_d, r) => (r.isDefault ? <Tag color="blue">{t('resolver.isDefault')}</Tag> : '–') },
        { title: t('resolver.active'), dataIndex: 'active', render: (_d, r) => <StatusTag status={r.active ? 'active' : 'disabled'} /> },
      ]}
      create={canWrite ? { fields, label: t('resolver.add') } : undefined}
      edit={canWrite ? { fields: () => fields } : undefined}
      rowActions={(r) =>
        canWrite
          ? [
              <Popconfirm key="del" title={t('resolver.confirmDelete')} onConfirm={() => void api.del(`/resolver/links/${r.id}`).then(() => notifySuccess(t('resolver.removed')))}>
                <a>{t('action.delete')}</a>
              </Popconfirm>,
            ]
          : []
      }
    />
  );
}
