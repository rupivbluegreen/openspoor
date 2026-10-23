import { Popconfirm, Switch, Typography } from 'antd';
import { DeleteOutlined } from '@ant-design/icons';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, formatDateTime, ProFormSwitch, ProFormText, type WatchSource } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';
import { useAction } from '../../lib/hooks';

type Row = WatchSource & Record<string, unknown>;

/** Regulator pages and specifications the reg-watch agent fetches; a change opens an impact case. */
export default function WatchedSources() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const remove = useAction((id: string) => api.del(`/watch-sources/${id}`), { invalidate: [['/watch-sources']] });
  const toggle = useAction((r: Row) => api.put(`/watch-sources/${r.id}`, { name: r.name, url: r.url, selector: r.selector, market: r.market, enabled: !r.enabled }), {
    invalidate: [['/watch-sources']],
    success: false,
  });
  const fields = (
    <>
      <ProFormText name="name" label={t('field.name')} rules={[{ required: true, max: 120 }]} />
      <ProFormText name="url" label={t('agents.watch.url')} rules={[{ required: true }, { pattern: /^https:\/\//i, message: 'https://…' }]} placeholder="https://" />
      <ProFormText name="selector" label={t('agents.watch.selector')} tooltip={t('agents.watch.selectorHint')} placeholder="#content" />
      <ProFormText name="market" label={t('agents.watch.market')} tooltip={t('agents.watch.marketHint')} fieldProps={{ maxLength: 8 }} placeholder="US, EU, …" />
      <ProFormSwitch name="enabled" label={t('agents.watch.enabled')} initialValue />
    </>
  );
  return (
    <CrudPage<Row>
      title={t('nav.watchedSources')}
      subTitle={t('agents.watch.subtitle')}
      path="/watch-sources"
      columns={[
        { title: t('field.name'), dataIndex: 'name' },
        { title: t('agents.watch.url'), dataIndex: 'url', ellipsis: true, render: (_d, r) => <Typography.Text copyable>{r.url}</Typography.Text> },
        { title: t('agents.watch.selector'), dataIndex: 'selector', render: (_d, r) => (r.selector ? <Typography.Text code>{r.selector}</Typography.Text> : '–') },
        { title: t('agents.watch.market'), dataIndex: 'market' },
        {
          title: t('agents.watch.enabled'),
          dataIndex: 'enabled',
          render: (_d, r) => <Switch size="small" checked={r.enabled} loading={toggle.isPending} onChange={() => toggle.mutate(r)} />,
        },
        { title: t('agents.watch.lastChecked'), dataIndex: 'lastCheckedAt', render: (_d, r) => (r.lastCheckedAt ? formatDateTime(r.lastCheckedAt) : t('agents.watch.never')) },
        { title: t('agents.watch.lastChanged'), dataIndex: 'lastChangedAt', render: (_d, r) => (r.lastChangedAt ? formatDateTime(r.lastChangedAt) : '–') },
        { title: t('agents.watch.lastError'), dataIndex: 'lastError', ellipsis: true, render: (_d, r) => (r.lastError ? <Typography.Text type="danger">{r.lastError}</Typography.Text> : '–') },
      ]}
      create={{ fields, label: t('agents.watch.add'), initialValues: { enabled: true } }}
      edit={{ fields, method: 'put', initialValues: (r) => ({ name: r.name, url: r.url, selector: r.selector, market: r.market, enabled: r.enabled }) }}
      rowActions={(r) => [
        <Popconfirm key="del" title={t('agents.watch.confirmDelete')} onConfirm={() => remove.mutateAsync(r.id).then(() => qc.invalidateQueries({ queryKey: ['/watch-sources'] }))}>
          <a>
            <DeleteOutlined /> {t('action.delete')}
          </a>
        </Popconfirm>,
      ]}
    />
  );
}
