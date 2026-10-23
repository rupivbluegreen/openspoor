import { useDeferredValue, useMemo, useState } from 'react';
import { Alert, Button, Card, Col, Empty, Input, Row, Select, Space, Table, Tabs, Tag, Typography, Upload } from 'antd';
import { BulbOutlined, SaveOutlined, UploadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, CodeView, PageContainer, prettyJson } from '@openspoor/ui/pro';
import { useAction } from '../../lib/hooks';
import { useDocTypeOptions, usePartnerOptions } from '../../lib/options';
import { type MapResult, type Mapping, type MappingField, type MappingOptions, type Transform } from './linkTypes';

const SIMPLE_OPS = ['trim', 'upper', 'lower'] as const;

/** Editable state of one target field. */
interface FieldState {
  source?: string;
  ops: string[];
  dateFrom?: string;
  constant?: string;
  lookup?: string;
}

function toFields(state: Record<string, FieldState>): MappingField[] {
  const out: MappingField[] = [];
  for (const [target, f] of Object.entries(state)) {
    const transforms: Transform[] = f.ops.map((op) => ({ op: op as Transform['op'] }));
    if (f.dateFrom) transforms.push({ op: 'date', from: f.dateFrom });
    if (f.lookup) {
      try {
        transforms.push({ op: 'lookup', table: JSON.parse(f.lookup) as Record<string, string> });
      } catch {
        /* invalid JSON is shown on the field */
      }
    }
    if (f.constant) transforms.push({ op: 'constant', value: f.constant });
    if (!f.source && !f.constant) continue;
    out.push({ target, source: f.source || undefined, transforms: transforms.length ? transforms : undefined });
  }
  return out;
}

function fromFields(fields: MappingField[]): Record<string, FieldState> {
  const out: Record<string, FieldState> = {};
  for (const f of fields) {
    const s: FieldState = { source: f.source, ops: [] };
    for (const t of f.transforms ?? []) {
      if ((SIMPLE_OPS as readonly string[]).includes(t.op)) s.ops.push(t.op);
      else if (t.op === 'date') s.dateFrom = t.from;
      else if (t.op === 'constant') s.constant = t.value;
      else if (t.op === 'lookup') s.lookup = JSON.stringify(t.table ?? {});
    }
    out[f.target] = s;
  }
  return out;
}

function parseColumns(text: string) {
  return text
    .split('\n')
    .map((l) => l.trim().split(':'))
    .filter((p) => p.length === 3 && p[0])
    .map(([name, start, length]) => ({ name, start: Number(start), length: Number(length) }));
}

export default function MappingStudio() {
  const { t } = useTranslation();
  const partners = usePartnerOptions();
  const docTypes = useDocTypeOptions(true);
  const [mappingId, setMappingId] = useState<string>();
  const [name, setName] = useState('');
  const [partnerId, setPartnerId] = useState<string>();
  const [docType, setDocType] = useState('order');
  const [format, setFormat] = useState<Mapping['format']>('csv');
  const [options, setOptions] = useState<MappingOptions>({});
  const [flatCols, setFlatCols] = useState('');
  const [content, setContent] = useState('');
  const [fields, setFields] = useState<Record<string, FieldState>>({});

  const saved = useQuery({ queryKey: ['/b2b/mappings'], queryFn: () => api.list<Mapping>('/b2b/mappings') });
  const targets = useQuery({
    queryKey: ['/b2b/mappings/suggest', docType, 'targets'],
    queryFn: () => api.post<{ targets: string[] }>('/b2b/mappings/suggest', { docType, columns: [] }),
  });

  const opts: MappingOptions = useMemo(() => (format === 'flat' ? { ...options, columns: parseColumns(flatCols) } : options), [format, options, flatCols]);
  const spec = useMemo(() => ({ docType, format, options: opts, fields: toFields(fields) }), [docType, format, opts, fields]);
  const deferred = useDeferredValue({ spec, content });
  const preview = useQuery({
    queryKey: ['/b2b/mappings/preview', deferred],
    queryFn: () => api.post<MapResult>('/b2b/mappings/preview', { mapping: deferred.spec, content: deferred.content }),
    enabled: deferred.content.length > 0 && (format !== 'flat' || (opts.columns?.length ?? 0) > 0),
    meta: { silent: true },
    retry: false,
  });
  const columns = preview.data?.columns ?? [];

  const suggest = useAction(() => api.post<{ fields: MappingField[] }>('/b2b/mappings/suggest', { docType, columns }), {
    success: false,
    onSuccess: (r) => setFields((cur) => ({ ...fromFields(r.fields), ...cur })),
  });
  const save = useAction(
    () => {
      const body = { name, partnerId, ...spec };
      return mappingId ? api.put<Mapping>(`/b2b/mappings/${mappingId}`, body) : api.post<Mapping>('/b2b/mappings', body);
    },
    { invalidate: [['/b2b/mappings']], onSuccess: (m) => setMappingId(m.id) },
  );

  const load = (id?: string) => {
    const m = saved.data?.items.find((x) => x.id === id);
    setMappingId(id);
    if (!m) return;
    setName(m.name);
    setPartnerId(m.partnerId);
    setDocType(m.docType);
    setFormat(m.format);
    setOptions(m.options ?? {});
    setFlatCols((m.options?.columns ?? []).map((c) => `${c.name}:${c.start}:${c.length}`).join('\n'));
    setFields(fromFields(m.fields));
  };

  const setField = (target: string, patch: Partial<FieldState>) =>
    setFields((cur) => ({ ...cur, [target]: { ...cur[target], ...patch, ops: patch.ops ?? cur[target]?.ops ?? [] } }));

  const colOptions = columns.map((c) => ({ value: c, label: c }));
  const errors = preview.data?.errors ?? [];
  const output = docType === 'epcis' ? preview.data?.events : preview.data?.documents;

  return (
    <PageContainer title={t('nav.mappingStudio')}>
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Card size="small">
          <Space wrap>
            <Select
              style={{ minWidth: 240 }}
              placeholder="Saved mappings"
              allowClear
              value={mappingId}
              onChange={load}
              options={(saved.data?.items ?? []).map((m) => ({ value: m.id, label: `${m.name} · ${m.docType} · ${m.format}` }))}
            />
            <Input placeholder={t('field.name')} value={name} onChange={(e) => setName(e.target.value)} style={{ width: 220 }} />
            <Select style={{ minWidth: 200 }} placeholder={t('field.partner')} allowClear value={partnerId} onChange={setPartnerId} options={partners} />
            <Select style={{ width: 240 }} value={docType} onChange={setDocType} options={docTypes.grouped} showSearch optionFilterProp="label" popupMatchSelectWidth={420} />
            <Select style={{ width: 120 }} value={format} onChange={setFormat} options={['csv', 'json', 'xml', 'x12', 'edifact', 'flat'].map((f) => ({ value: f, label: f.toUpperCase() }))} />
            <Button type="primary" icon={<SaveOutlined />} disabled={!name || spec.fields.length === 0} loading={save.isPending} onClick={() => save.mutate(undefined)}>
              {t('action.save')}
            </Button>
          </Space>
        </Card>
        <Row gutter={16}>
          <Col xs={24} lg={10}>
            <Card
              title="Sample file"
              size="small"
              extra={
                <Upload
                  accept=".csv,.txt,.json,.dat,.xml,.edi,.x12,.edifact"
                  showUploadList={false}
                  beforeUpload={(f) => {
                    void f.text().then(setContent);
                    return false;
                  }}
                >
                  <Button size="small" icon={<UploadOutlined />}>{t('action.upload')}</Button>
                </Upload>
              }
            >
              <Space direction="vertical" style={{ width: '100%' }}>
                {format === 'csv' && (
                  <Space wrap>
                    <Input addonBefore="Delimiter" style={{ width: 160 }} value={options.delimiter ?? ''} placeholder=", ; tab" onChange={(e) => setOptions({ ...options, delimiter: e.target.value || undefined })} />
                    <Select
                      value={options.noHeader ? 'none' : 'header'}
                      onChange={(v) => setOptions({ ...options, noHeader: v === 'none' })}
                      options={[{ value: 'header', label: 'First row is a header' }, { value: 'none', label: 'No header row' }]}
                    />
                  </Space>
                )}
                {(format === 'json' || format === 'xml') && (
                  <Space wrap>
                    <Input addonBefore="Records" placeholder="data.orders" value={options.recordsPath ?? ''} onChange={(e) => setOptions({ ...options, recordsPath: e.target.value || undefined })} />
                    <Input addonBefore="Lines" placeholder="items" value={options.linesPath ?? ''} onChange={(e) => setOptions({ ...options, linesPath: e.target.value || undefined })} />
                  </Space>
                )}
                {(format === 'x12' || format === 'edifact') && (
                  <Input
                    addonBefore={t('mapping.lineSegment')}
                    style={{ width: 280 }}
                    placeholder={t('mapping.lineSegmentHint')}
                    value={options.linesPath ?? ''}
                    onChange={(e) => setOptions({ ...options, linesPath: e.target.value.toUpperCase() || undefined })}
                  />
                )}
                {format === 'flat' && (
                  <Input.TextArea rows={3} placeholder={'po:1:10\ngtin:11:14\nqty:25:5'} value={flatCols} onChange={(e) => setFlatCols(e.target.value)} />
                )}
                <Input.TextArea rows={10} value={content} onChange={(e) => setContent(e.target.value)} placeholder="Paste or upload a sample file" style={{ fontFamily: 'monospace' }} />
                {preview.error && <Alert type="error" showIcon title={(preview.error as Error).message} />}
              </Space>
            </Card>
          </Col>
          <Col xs={24} lg={14}>
            <Card
              title="Field mapping"
              size="small"
              extra={
                <Button size="small" icon={<BulbOutlined />} disabled={columns.length === 0} onClick={() => suggest.mutate(undefined)}>
                  Suggest from headers
                </Button>
              }
            >
              <Table
                size="small"
                rowKey="target"
                pagination={false}
                scroll={{ y: 420, x: 760 }}
                dataSource={(targets.data?.targets ?? []).map((target) => ({ target }))}
                columns={[
                  { title: 'Target', dataIndex: 'target', width: 160, render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
                  {
                    title: 'Source column',
                    width: 180,
                    render: (_v, r) => (
                      <Select size="small" allowClear showSearch style={{ width: '100%' }} options={colOptions} value={fields[r.target]?.source} onChange={(v) => setField(r.target, { source: v })} />
                    ),
                  },
                  {
                    title: 'Transforms',
                    width: 170,
                    render: (_v, r) => (
                      <Select
                        size="small"
                        mode="multiple"
                        style={{ width: '100%' }}
                        options={SIMPLE_OPS.map((o) => ({ value: o, label: o }))}
                        value={fields[r.target]?.ops ?? []}
                        onChange={(v) => setField(r.target, { ops: v })}
                      />
                    ),
                  },
                  {
                    title: 'Date pattern',
                    width: 120,
                    render: (_v, r) => <Input size="small" placeholder="DD/MM/YYYY" value={fields[r.target]?.dateFrom ?? ''} onChange={(e) => setField(r.target, { dateFrom: e.target.value })} />,
                  },
                  {
                    title: 'Lookup (JSON)',
                    width: 140,
                    render: (_v, r) => <Input size="small" placeholder='{"A":"B"}' value={fields[r.target]?.lookup ?? ''} onChange={(e) => setField(r.target, { lookup: e.target.value })} />,
                  },
                  {
                    title: 'Constant',
                    width: 120,
                    render: (_v, r) => <Input size="small" value={fields[r.target]?.constant ?? ''} onChange={(e) => setField(r.target, { constant: e.target.value })} />,
                  },
                ]}
              />
            </Card>
          </Col>
        </Row>
        <Card size="small" title={`Preview${preview.data ? ` · ${preview.data.rows} rows` : ''}`} loading={preview.isFetching && !preview.data}>
          {!preview.data ? (
            <Empty description="Add a sample file to preview" />
          ) : (
            <Tabs
              items={[
                {
                  key: 'out',
                  label: (
                    <span>
                      Result {errors.length > 0 ? <Tag color="error">{errors.length} errors</Tag> : <Tag color="success">OK</Tag>}
                    </span>
                  ),
                  children: (
                    <Row gutter={16}>
                      <Col xs={24} lg={14}>
                        <CodeView code={prettyJson(output ?? [])} maxHeight={420} />
                      </Col>
                      <Col xs={24} lg={10}>
                        <Table
                          size="small"
                          rowKey={(r) => `${r.row}-${r.field}-${r.message}`}
                          dataSource={errors}
                          pagination={{ pageSize: 10 }}
                          columns={[
                            { title: 'Row', dataIndex: 'row', width: 60 },
                            { title: 'Field', dataIndex: 'field', width: 130 },
                            { title: 'Message', dataIndex: 'message' },
                          ]}
                        />
                      </Col>
                    </Row>
                  ),
                },
                {
                  key: 'sample',
                  label: 'Sample rows',
                  children: (
                    <Table
                      size="small"
                      rowKey={(_r, i) => String(i)}
                      scroll={{ x: true }}
                      dataSource={preview.data.sample}
                      pagination={false}
                      columns={columns.map((c) => ({ title: c, key: c, render: (_v: unknown, r: Record<string, string>) => r[c] }))}
                    />
                  ),
                },
              ]}
            />
          )}
        </Card>
      </Space>
    </PageContainer>
  );
}
