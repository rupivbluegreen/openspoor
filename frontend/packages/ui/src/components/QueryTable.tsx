import { useEffect, useMemo, useRef, useState } from 'react';
import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { theme } from 'antd';
import { ProTable, type ColumnsState, type ProColumns, type ProFormInstance, type ProTableProps } from '../pro';
import type { Page } from '../api/client';
import { downloadCsv } from '../csv';
import { ExportCsvButton, ViewsDropdown, type SavedView } from './TableTools';

export type FetchParams = { current: number; pageSize: number } & Record<string, unknown>;

export type QueryTableProps<T extends Record<string, unknown>> = Omit<
  ProTableProps<T, Record<string, unknown>>,
  'request' | 'dataSource' | 'loading' | 'pagination' | 'params' | 'onSubmit' | 'onReset' | 'columns'
> & {
  /** ProTable columns; `exportValue` sets what CSV export writes for the column. */
  columns?: (ProColumns<T> & ExportValue<T>)[];
  /** TanStack Query key prefix. Invalidate it after mutations to refresh. */
  queryKey: readonly unknown[];
  fetcher: (params: FetchParams) => Promise<Page<T>>;
  /** When true the server paginates (current/pageSize are passed to the fetcher). */
  serverPagination?: boolean;
  pageSize?: number;
  /** Extra params merged into every fetch (part of the query key). */
  extraParams?: Record<string, unknown>;
  /** Stable table id: enables saved views and CSV export (also the export file name). */
  tableId?: string;
  /** Highlights (and scrolls to) one record, e.g. from a `?id=` link. */
  focus?: { id: string; match?: (r: T) => boolean; onFound?: (r: T) => void };
};

const FOCUS_CLASS = 'openspoor-row-focus';

function clean(v: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(v).filter(([, x]) => x !== undefined && x !== null && x !== ''));
}

/** Same key ProTable uses for its column settings map. */
function columnKey(key: unknown, index: number): string {
  if (key !== undefined && key !== null && key !== '') return Array.isArray(key) ? key.join('-') : String(key);
  return String(index);
}

function valueAt(row: Record<string, unknown>, dataIndex: unknown): unknown {
  const path = Array.isArray(dataIndex) ? dataIndex : [dataIndex];
  return path.reduce<unknown>((o, k) => (o && typeof o === 'object' ? (o as Record<string, unknown>)[String(k)] : undefined), row);
}

/** A column may give the value to export when the raw field is an id or the cell is computed. */
export type ExportValue<T> = { exportValue?: (row: T) => unknown };

/** Header + cells of the loaded rows for the visible data columns: `exportValue` when the column
 * has one, otherwise the raw field value (not the rendered cell). */
export function tableToCsvRows<T extends Record<string, unknown>>(columns: ProColumns<T>[], rows: T[], state?: Record<string, ColumnsState>) {
  const cols = columns
    .map((c, i) => ({ c, key: columnKey(c.key ?? c.dataIndex, i), i }))
    .filter(({ c, key }) => (c.dataIndex !== undefined || exportOf(c)) && c.valueType !== 'option' && !c.hideInTable && state?.[key]?.show !== false)
    .sort((a, b) => (state?.[a.key]?.order ?? a.i) - (state?.[b.key]?.order ?? b.i));
  const header = cols.map(({ c }) => (typeof c.title === 'string' || typeof c.title === 'number' ? c.title : columnKey(c.dataIndex, 0)));
  return { header, body: rows.map((r) => cols.map(({ c }) => (exportOf(c) ? exportOf(c)!(r) : valueAt(r, c.dataIndex)))) };
}

function exportOf<T>(c: ProColumns<T>): ((row: T) => unknown) | undefined {
  return (c as ProColumns<T> & ExportValue<T>).exportValue;
}

/** ProTable whose data comes from TanStack Query (cache, retries, invalidation, error notifications). */
export function QueryTable<T extends Record<string, unknown>>(props: QueryTableProps<T>) {
  const { queryKey, fetcher, serverPagination = false, pageSize = 20, extraParams, search, options, tableId, focus, ...rest } = props;
  const { token } = theme.useToken();
  const [form, setForm] = useState<Record<string, unknown>>({});
  const [page, setPage] = useState({ current: 1, pageSize });
  const [colState, setColState] = useState<Record<string, ColumnsState> | undefined>(undefined);
  const ownFormRef = useRef<ProFormInstance | undefined>(undefined);
  const formRef = (rest.formRef as typeof ownFormRef | undefined) ?? ownFormRef;
  const wrapRef = useRef<HTMLDivElement>(null);
  const focused = useRef<string | null>(null);
  const params = useMemo(() => ({ ...clean(extraParams ?? {}), ...form }), [extraParams, form]);

  const q = useQuery({
    queryKey: [...queryKey, params, serverPagination ? page : null],
    queryFn: () => fetcher({ ...params, ...page }),
    placeholderData: keepPreviousData,
  });
  const items = useMemo(() => q.data?.items ?? [], [q.data]);

  const isFocus = (r: T) => !!focus && (focus.match ? focus.match(r) : String(r.id) === focus.id);
  useEffect(() => {
    if (!focus || focused.current === focus.id) return;
    const idx = items.findIndex((r) => (focus.match ? focus.match(r) : String(r.id) === focus.id));
    if (idx < 0) return;
    focused.current = focus.id;
    if (!serverPagination) setPage((p) => ({ ...p, current: Math.floor(idx / p.pageSize) + 1 }));
    focus.onFound?.(items[idx]!);
    const timer = setTimeout(() => wrapRef.current?.querySelector(`.${FOCUS_CLASS}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' }), 150);
    return () => clearTimeout(timer);
  }, [focus, items, serverPagination]);

  const callerRowClass = rest.rowClassName;
  const callerToolbar = rest.toolBarRender;
  const columns = (rest.columns ?? []) as ProColumns<T>[];

  const applyView = (v: SavedView) => {
    const p = clean(v.params ?? {});
    formRef.current?.resetFields?.();
    formRef.current?.setFieldsValue?.(p);
    setForm(p);
    setPage((x) => ({ ...x, current: 1 }));
    if (v.columns) setColState(v.columns);
  };

  return (
    <div ref={wrapRef}>
      {focus && <style>{`.${FOCUS_CLASS} > td { background: ${token.colorPrimaryBg} !important; }`}</style>}
      <ProTable<T, Record<string, unknown>>
        cardBordered
        dateFormatter="string"
        {...rest}
        formRef={formRef}
        search={search ?? false}
        dataSource={items}
        loading={q.isFetching}
        rowClassName={(r, i, indent) =>
          [typeof callerRowClass === 'function' ? callerRowClass(r, i, indent) : callerRowClass, isFocus(r) ? FOCUS_CLASS : '']
            .filter(Boolean)
            .join(' ')
        }
        columnsState={rest.columnsState ?? (tableId ? { value: colState, onChange: setColState } : undefined)}
        toolBarRender={
          callerToolbar === false
            ? false
            : (action, rows) => [
                ...(tableId
                  ? [
                      <ViewsDropdown key="views" tableId={tableId} current={() => ({ params: form, columns: colState })} onApply={applyView} />,
                      <ExportCsvButton
                        key="export"
                        count={items.length}
                        onExport={() => {
                          const { header, body } = tableToCsvRows(columns, items, colState);
                          downloadCsv(tableId, header, body);
                        }}
                      />,
                    ]
                  : []),
                ...(callerToolbar?.(action, rows) ?? []),
              ]
        }
        onSubmit={(v) => {
          setForm(clean(v as Record<string, unknown>));
          setPage((p) => ({ ...p, current: 1 }));
        }}
        onReset={() => {
          setForm({});
          setPage((p) => ({ ...p, current: 1 }));
        }}
        options={options === false ? false : { reload: () => void q.refetch(), density: true, setting: true, ...(options || {}) }}
        pagination={{
          current: page.current,
          pageSize: page.pageSize,
          showSizeChanger: true,
          onChange: (current, size) => setPage({ current, pageSize: size }),
          ...(serverPagination ? { total: q.data?.total ?? 0 } : {}),
        }}
      />
    </div>
  );
}
