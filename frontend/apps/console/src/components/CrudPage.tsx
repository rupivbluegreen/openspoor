import { useState, type ReactNode } from 'react';
import { Button } from 'antd';
import { EditOutlined, PlusOutlined } from '@ant-design/icons';
import { useSearchParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, ModalForm, type Query, PageContainer, QueryTable, type ProColumns, type QueryTableProps } from '@openspoor/ui/pro';
import { useSubmit } from '../lib/hooks';

type Row = Record<string, unknown>;

export interface CrudForm<T> {
  title?: ReactNode;
  fields: ReactNode | ((record?: T) => ReactNode);
  /** Map form values to the request body. */
  transform?: (values: Row, record?: T) => unknown;
  initialValues?: Row | ((record: T) => Row);
  width?: number;
}

export interface CrudPageProps<T extends Row> {
  title: ReactNode;
  subTitle?: ReactNode;
  /** List endpoint, e.g. `/orgs`. Also used for POST unless `createPath` is set. */
  path: string;
  queryKey?: readonly unknown[];
  columns: ProColumns<T>[];
  rowKey?: string;
  create?: CrudForm<T> & { path?: string; label?: ReactNode };
  edit?: CrudForm<T> & { method?: 'put' | 'patch'; path?: (r: T) => string };
  /** Additional per-row actions appended to the option column. */
  rowActions?: (r: T) => ReactNode[];
  toolbar?: ReactNode[];
  extraParams?: Row;
  table?: Partial<QueryTableProps<T>>;
  /** Saved views / CSV export id; defaults to the list path (`/food/lots` → `food-lots`). */
  tableId?: string;
  header?: ReactNode;
  children?: ReactNode;
  wrap?: boolean;
}

/** Generic list (ProTable via TanStack Query) + create/edit (ModalForm) page. */
export function CrudPage<T extends Row>(props: CrudPageProps<T>) {
  const { t } = useTranslation();
  const key = props.queryKey ?? [props.path];
  const rowKey = props.rowKey ?? 'id';
  const tableId = props.tableId ?? props.path.replace(/^\/+/, '').replace(/[^a-zA-Z0-9]+/g, '-');
  // `?id=` (global search links) highlights that record and, when editable, opens it.
  const [search] = useSearchParams();
  const focusId = search.get('id');
  const [editing, setEditing] = useState<T | null>(null);

  const create = useSubmit((v: Row) => api.post(props.create?.path ?? props.path, props.create?.transform ? props.create.transform(v) : v), {
    invalidate: [key],
  });
  const update = useSubmit(
    ({ record, values }: { record: T; values: Row }) => {
      const e = props.edit!;
      const url = e.path ? e.path(record) : `${props.path}/${String(record[rowKey])}`;
      const body = e.transform ? e.transform(values, record) : values;
      return e.method === 'patch' ? api.patch(url, body) : api.put(url, body);
    },
    { invalidate: [key] },
  );

  const columns: ProColumns<T>[] = [...props.columns];
  if (props.edit || props.rowActions) {
    columns.push({
      title: t('common.actions'),
      valueType: 'option',
      fixed: 'right',
      width: 160,
      render: (_d, record) => [
        props.edit ? (
          <a key="edit" onClick={() => setEditing(record)}>
            <EditOutlined /> {t('action.edit')}
          </a>
        ) : null,
        ...(props.rowActions?.(record) ?? []),
      ],
    });
  }

  const toolbar = [
    ...(props.toolbar ?? []),
    props.create ? (
      <ModalForm<Row>
        key="create"
        title={props.create.title ?? t('action.create')}
        width={props.create.width ?? 560}
        trigger={
          <Button type="primary" icon={<PlusOutlined />}>
            {props.create.label ?? t('action.create')}
          </Button>
        }
        modalProps={{ destroyOnHidden: true }}
        initialValues={typeof props.create.initialValues === 'function' ? undefined : props.create.initialValues}
        onFinish={create}
      >
        {typeof props.create.fields === 'function' ? props.create.fields() : props.create.fields}
      </ModalForm>
    ) : null,
  ].filter(Boolean);

  const table = (
    <QueryTable<T>
      rowKey={rowKey}
      queryKey={key}
      fetcher={({ current: _c, pageSize: _s, ...q }) => api.list<T>(props.path, q as Query)}
      extraParams={props.extraParams}
      columns={columns}
      tableId={tableId}
      focus={focusId ? { id: focusId, onFound: props.edit ? setEditing : undefined } : undefined}
      toolBarRender={() => toolbar}
      scroll={{ x: 'max-content' }}
      {...props.table}
    />
  );

  const edit = props.edit;
  const editForm =
    edit && editing ? (
      <ModalForm<Row>
        key={String(editing[rowKey])}
        open
        onOpenChange={(o) => !o && setEditing(null)}
        title={edit.title ?? t('action.edit')}
        width={edit.width ?? 560}
        modalProps={{ destroyOnHidden: true }}
        initialValues={typeof edit.initialValues === 'function' ? edit.initialValues(editing) : (edit.initialValues ?? editing)}
        onFinish={async (values) => {
          const ok = await update({ record: editing, values });
          if (ok) setEditing(null);
          return ok;
        }}
      >
        {typeof edit.fields === 'function' ? edit.fields(editing) : edit.fields}
      </ModalForm>
    ) : null;

  if (props.wrap === false)
    return (
      <>
        {table}
        {editForm}
      </>
    );
  return (
    <PageContainer title={props.title} subTitle={props.subTitle} content={props.header}>
      {props.children}
      {table}
      {editForm}
    </PageContainer>
  );
}
