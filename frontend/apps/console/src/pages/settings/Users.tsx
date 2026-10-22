import { Alert, Tag } from 'antd';
import { useTranslation } from 'react-i18next';
import { LANGS, ProFormSelect, ProFormText, ProFormTextArea, ROLES, StatusTag, type User } from '@openspoor/ui/pro';
import { CrudPage } from '../../components/CrudPage';

export default function Users() {
  const { t } = useTranslation();
  const roleOptions = ROLES.map((r) => ({ value: r, label: r }));
  return (
    <CrudPage<User & Record<string, unknown>>
      title={t('nav.users')}
      path="/users"
      columns={[
        { title: t('field.email'), dataIndex: 'email', copyable: true },
        { title: t('field.name'), dataIndex: 'name' },
        { title: t('field.roles'), dataIndex: 'roles', render: (_d, r) => r.roles?.map((x) => <Tag key={x}>{x}</Tag>) },
        { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
        { title: t('field.locale'), dataIndex: 'locale' },
        { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
      ]}
      create={{
        fields: (
          <>
            <ProFormText name="email" label={t('field.email')} rules={[{ required: true }, { type: 'email' }]} />
            <ProFormText name="name" label={t('field.name')} />
            <ProFormSelect name="roles" label={t('field.roles')} mode="multiple" options={roleOptions} rules={[{ required: true }]} />
            <ProFormText.Password
              name="password"
              label={t('field.password')}
              tooltip="Leave empty for SSO-only users"
              rules={[{ min: 10, message: 'At least 10 characters' }]}
            />
            <ProFormSelect name="locale" label={t('field.locale')} initialValue="en" options={LANGS.map((l) => ({ value: l.code, label: l.label }))} />
          </>
        ),
        transform: (v) => ({ ...v, password: v.password || undefined }),
      }}
      edit={{
        method: 'patch',
        initialValues: (r) => ({ name: r.name, roles: r.roles, status: r.status, locale: r.locale }),
        fields: (
          <>
            <Alert type="info" showIcon title={t('common.audited')} style={{ marginBottom: 16 }} />
            <ProFormText name="name" label={t('field.name')} />
            <ProFormSelect name="roles" label={t('field.roles')} mode="multiple" options={roleOptions} rules={[{ required: true }]} />
            <ProFormSelect name="status" label={t('field.status')} options={['active', 'disabled'].map((s) => ({ value: s, label: s }))} />
            <ProFormSelect name="locale" label={t('field.locale')} options={LANGS.map((l) => ({ value: l.code, label: l.label }))} />
            <ProFormTextArea name="reason" label={t('field.reason')} rules={[{ required: true }]} />
          </>
        ),
      }}
    />
  );
}
