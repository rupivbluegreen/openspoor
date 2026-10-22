import { useMemo, useState } from 'react';
import { Button, Card, Col, Drawer, Empty, Form, Row, Tabs } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, JsonSchemaFields, ModalForm, PageContainer, prettyJson, ProForm, ProFormList, ProFormSelect, ProFormSwitch, ProFormText,
  ProFormTextArea, QueryTable, type FormDef, type JsonSchema,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';

interface FieldDef {
  name: string;
  title?: string;
  type: 'string' | 'number' | 'integer' | 'boolean' | 'date' | 'textarea' | 'select';
  required?: boolean;
  options?: string[];
}

export function toSchema(title: string | undefined, fields: FieldDef[] = []): JsonSchema {
  const properties: Record<string, JsonSchema> = {};
  for (const f of fields) {
    if (!f?.name) continue;
    const base: JsonSchema = { title: f.title || f.name };
    switch (f.type) {
      case 'select':
        properties[f.name] = { ...base, type: 'string', enum: f.options ?? [] };
        break;
      case 'textarea':
        properties[f.name] = { ...base, type: 'string', format: 'textarea' };
        break;
      case 'date':
        properties[f.name] = { ...base, type: 'string', format: 'date' };
        break;
      default:
        properties[f.name] = { ...base, type: f.type };
    }
  }
  return { type: 'object', title, properties, required: fields.filter((f) => f?.required && f.name).map((f) => f.name) };
}

function FormBuilder({ onSaved }: { onSaved: () => void }) {
  const { t } = useTranslation();
  const [form] = Form.useForm<{ name: string; fields: FieldDef[] }>();
  const name = Form.useWatch('name', form);
  const fields = Form.useWatch('fields', form);
  const schema = useMemo(() => toSchema(name, fields), [name, fields]);
  const save = useSubmit(() => api.post('/forms', { name, schema }), { invalidate: [['/forms']], onSuccess: onSaved });
  return (
    <Row gutter={16}>
      <Col xs={24} lg={12}>
        <Card title="Fields" size="small">
          <ProForm form={form} initialValues={{ fields: [{ name: 'lot', title: 'Lot number', type: 'string', required: true }] }} onFinish={save} submitter={{ searchConfig: { submitText: t('action.save') } }}>
            <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
            <ProFormList name="fields" creatorButtonProps={{ creatorButtonText: t('action.add') }} creatorRecord={{ type: 'string' }}>
              {(_f, _i, action) => (
                <ProForm.Group>
                  <ProFormText name="name" label="Key" width="xs" rules={[{ required: true, pattern: /^[A-Za-z_][A-Za-z0-9_]*$/ }]} />
                  <ProFormText name="title" label="Label" width="sm" />
                  <ProFormSelect name="type" label={t('field.type')} width="xs" options={['string', 'textarea', 'number', 'integer', 'boolean', 'date', 'select']} />
                  <ProFormSwitch name="required" label={t('common.required')} />
                  {action.getCurrentRowData()?.type === 'select' && <ProFormSelect name="options" label="Options" mode="tags" width="md" />}
                </ProForm.Group>
              )}
            </ProFormList>
          </ProForm>
        </Card>
      </Col>
      <Col xs={24} lg={12}>
        <Card title={t('action.preview')} size="small" style={{ marginBottom: 16 }}>
          {Object.keys(schema.properties ?? {}).length ? (
            <ProForm submitter={false} layout="vertical">
              <JsonSchemaFields schema={schema} />
            </ProForm>
          ) : (
            <Empty />
          )}
        </Card>
        <CodeView title="JSON schema" code={prettyJson(schema)} filename={`${name || 'form'}.schema.json`} maxHeight={260} />
      </Col>
    </Row>
  );
}

function Forms() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [preview, setPreview] = useState<FormDef | null>(null);
  return (
    <>
      <QueryTable<FormDef & Record<string, unknown>>
        rowKey={(r) => String(r.id ?? r.name)}
        queryKey={['/forms']}
        fetcher={() => api.list('/forms')}
        columns={[
          { title: t('field.name'), dataIndex: 'name' },
          { title: 'Fields', render: (_d, r) => Object.keys(r.schema?.properties ?? {}).length },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
          { title: t('common.actions'), valueType: 'option', render: (_d, r) => [<a key="p" onClick={() => setPreview(r)}>{t('action.preview')}</a>] },
        ]}
        toolBarRender={() => [<Button key="new" type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>{t('action.create')}</Button>]}
      />
      <Drawer open={open} onClose={() => setOpen(false)} title={t('nav.formsWorkflows')} size="large" destroyOnHidden>
        <FormBuilder onSaved={() => setOpen(false)} />
      </Drawer>
      <Drawer open={!!preview} onClose={() => setPreview(null)} title={preview?.name} size="default" destroyOnHidden>
        <ProForm submitter={false} layout="vertical">
          <JsonSchemaFields schema={preview?.schema} />
        </ProForm>
      </Drawer>
    </>
  );
}

function Workflows() {
  const { t } = useTranslation();
  const create = useSubmit((v: { name: string; definition: string }) => api.post('/workflows', { name: v.name, ...JSON.parse(v.definition) }), { invalidate: [['/workflows']] });
  return (
    <QueryTable<Record<string, unknown>>
      rowKey={(r) => String(r.id ?? r.name)}
      queryKey={['/workflows']}
      fetcher={() => api.list('/workflows')}
      columns={[
        { title: t('field.name'), dataIndex: 'name' },
        { title: 'Trigger', dataIndex: 'trigger', render: (v) => (v && typeof v === 'object' ? JSON.stringify(v) : String(v ?? '–')) },
        { title: 'Steps', dataIndex: 'steps', render: (v) => (Array.isArray(v) ? v.length : '–') },
        { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
      ]}
      expandable={{ expandedRowRender: (r) => <CodeView code={prettyJson(r)} maxHeight={260} /> }}
      toolBarRender={() => [
        <ModalForm<{ name: string; definition: string }>
          key="new"
          title="Workflow"
          width={720}
          trigger={<Button type="primary" icon={<PlusOutlined />}>{t('action.create')}</Button>}
          modalProps={{ destroyOnHidden: true }}
          initialValues={{ definition: prettyJson({ trigger: { event: 'alert.created', kind: 'regulatory' }, steps: [{ type: 'form', form: 'investigation' }, { type: 'approval', role: 'Compliance', esignature: true }] }) }}
          onFinish={create}
        >
          <ProFormText name="name" label={t('field.name')} rules={[{ required: true }]} />
          <ProFormTextArea name="definition" label="Definition (JSON)" rules={[{ required: true }, { validator: async (_r, v) => { JSON.parse(v as string); } }]} fieldProps={{ rows: 14, style: { fontFamily: 'monospace' } }} />
        </ModalForm>,
      ]}
    />
  );
}

export default function Designer() {
  const { t } = useTranslation();
  return (
    <PageContainer title={t('nav.formsWorkflows')}>
      <Tabs items={[{ key: 'forms', label: 'Forms', children: <Forms /> }, { key: 'wf', label: 'Workflows', children: <Workflows /> }]} />
    </PageContainer>
  );
}
