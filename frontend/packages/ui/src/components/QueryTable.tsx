import { useMemo, useState } from 'react';
import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { ProTable, type ProTableProps } from '../pro';
import type { Page } from '../api/client';

export type FetchParams = { current: number; pageSize: number } & Record<string, unknown>;

export type QueryTableProps<T extends Record<string, unknown>> = Omit<
  ProTableProps<T, Record<string, unknown>>,
  'request' | 'dataSource' | 'loading' | 'pagination' | 'params' | 'onSubmit' | 'onReset'
> & {
  /** TanStack Query key prefix. Invalidate it after mutations to refresh. */
  queryKey: readonly unknown[];
  fetcher: (params: FetchParams) => Promise<Page<T>>;
  /** When true the server paginates (current/pageSize are passed to the fetcher). */
  serverPagination?: boolean;
  pageSize?: number;
  /** Extra params merged into every fetch (part of the query key). */
  extraParams?: Record<string, unknown>;
};

function clean(v: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(v).filter(([, x]) => x !== undefined && x !== null && x !== ''));
}

/** ProTable whose data comes from TanStack Query (cache, retries, invalidation, error notifications). */
export function QueryTable<T extends Record<string, unknown>>(props: QueryTableProps<T>) {
  const { queryKey, fetcher, serverPagination = false, pageSize = 20, extraParams, search, options, ...rest } = props;
  const [form, setForm] = useState<Record<string, unknown>>({});
  const [page, setPage] = useState({ current: 1, pageSize });
  const params = useMemo(() => ({ ...clean(extraParams ?? {}), ...form }), [extraParams, form]);

  const q = useQuery({
    queryKey: [...queryKey, params, serverPagination ? page : null],
    queryFn: () => fetcher({ ...params, ...page }),
    placeholderData: keepPreviousData,
  });

  return (
    <ProTable<T, Record<string, unknown>>
      cardBordered
      dateFormatter="string"
      {...rest}
      search={search ?? false}
      dataSource={q.data?.items ?? []}
      loading={q.isFetching}
      onSubmit={(v) => {
        setForm(clean(v as Record<string, unknown>));
        setPage((p) => ({ ...p, current: 1 }));
      }}
      onReset={() => {
        setForm({});
        setPage((p) => ({ ...p, current: 1 }));
      }}
      options={options === false ? false : { reload: () => void q.refetch(), density: true, setting: true, ...(options || {}) }}
      pagination={
        serverPagination
          ? {
              current: page.current,
              pageSize: page.pageSize,
              total: q.data?.total ?? 0,
              showSizeChanger: true,
              onChange: (current, size) => setPage({ current, pageSize: size }),
            }
          : { defaultPageSize: pageSize, showSizeChanger: true }
      }
    />
  );
}
