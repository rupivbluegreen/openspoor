import type { ReactNode } from 'react';
import { Button } from 'antd';
import { EditOutlined, PlusOutlined } from '@ant-design/icons';
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
  header?: ReactNode;
  children?: ReactNode;
  wrap?: boolean;
}

/** Generic list (ProTable via TanStack Query) + create/edit (ModalForm) page. */
export function CrudPage<T extends Row>(props: CrudPageProps<T>) {
  const { t } = useTranslation();
  const key = props.queryKey ?? [props.path];
  const rowKey = props.rowKey ?? 'id';

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
          <ModalForm<Row>
            key="edit"
            title={props.edit.title ?? t('action.edit')}
            width={props.edit.width ?? 560}
            trigger={
              <a>
                <EditOutlined /> {t('action.edit')}
              </a>
            }
            modalProps={{ destroyOnHidden: true }}
            initialValues={typeof props.edit.initialValues === 'function' ? props.edit.initialValues(record) : (props.edit.initialValues ?? record)}
            onFinish={(values) => update({ record, values })}
          >
            {typeof props.edit.fields === 'function' ? props.edit.fields(record) : props.edit.fields}
          </ModalForm>
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
      toolBarRender={() => toolbar}
      scroll={{ x: 'max-content' }}
      {...props.table}
    />
  );

  if (props.wrap === false) return table;
  return (
    <PageContainer title={props.title} subTitle={props.subTitle} content={props.header}>
      {props.children}
      {table}
    </PageContainer>
  );
}
