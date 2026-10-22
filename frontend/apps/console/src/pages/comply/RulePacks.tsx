import { useTranslation } from 'react-i18next';
import { api, CodeView, PageContainer, prettyJson, QueryTable, type RulePack } from '@openspoor/ui/pro';

export default function RulePacks() {
  const { t } = useTranslation();
  return (
    <PageContainer title={t('nav.rulePacks')} subTitle="Versioned YAML country rule packs">
      <QueryTable<RulePack & Record<string, unknown>>
        rowKey={(r) => String(r.id ?? `${r.name}@${r.version}`)}
        queryKey={['/rulepacks']}
        fetcher={() => api.list('/rulepacks')}
        expandable={{ expandedRowRender: (r) => <CodeView code={typeof r.yaml === 'string' ? r.yaml : prettyJson(r)} filename={`${r.name ?? r.id}-${r.version ?? ''}.yaml`} maxHeight={400} /> }}
        columns={[
          { title: t('field.name'), dataIndex: 'name' },
          { title: t('field.country'), dataIndex: 'country' },
          { title: t('field.version'), dataIndex: 'version' },
          { title: t('field.description'), dataIndex: 'description', ellipsis: true },
        ]}
      />
    </PageContainer>
  );
}
