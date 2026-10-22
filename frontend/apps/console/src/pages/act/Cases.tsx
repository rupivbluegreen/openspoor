import { Tag } from 'antd';
import { useTranslation } from 'react-i18next';
import { ProFormSelect, ProFormText, ProFormTextArea, statusColor, StatusTag, type Case } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';

const PRIORITIES = ['critical', 'high', 'medium', 'low'];
const STATUSES = ['open', 'in_progress', 'waiting', 'resolved', 'closed'];

export default function Cases() {
  const { t } = useTranslation();
  return (
    <CrudPage<Case & Record<string, unknown>>
      title={t('nav.cases')}
      path="/cases"
      columns={[
        { title: t('field.title'), dataIndex: 'title' },
        { title: t('field.kind'), dataIndex: 'kind' },
        { title: t('field.priority'), dataIndex: 'priority', render: (_d, r) => <Tag color={statusColor(String(r.priority))}>{String(r.priority ?? '–')}</Tag> },
        { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
        { title: 'Assignee', dataIndex: 'assignee' },
        { title: t('field.entity'), render: (_d, r) => (r.entity ? `${r.entity}:${r.entityId ?? ''}` : '–') },
        { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
      ]}
      create={{
        initialValues: { priority: 'medium' },
        fields: (
          <>
            <ProFormText name="title" label={t('field.title')} rules={[{ required: true }]} />
            <ProFormText name="kind" label={t('field.kind')} placeholder="suspect_product, excursion, mismatch…" />
            <ProFormSelect name="priority" label={t('field.priority')} options={PRIORITIES} />
            <ProFormTextArea name="description" label={t('field.description')} />
          </>
        ),
      }}
      edit={{
        method: 'patch',
        initialValues: (r) => ({ status: r.status, priority: r.priority, assignee: r.assignee }),
        fields: (
          <>
            <ProFormSelect name="status" label={t('field.status')} options={STATUSES} />
            <ProFormSelect name="priority" label={t('field.priority')} options={PRIORITIES} />
            <ProFormText name="assignee" label="Assignee" />
            <ProFormTextArea name="note" label="Note" />
          </>
        ),
      }}
    />
  );
}
