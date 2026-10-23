import { useState } from 'react';
import { Alert, Button, Card, Empty, Input, List, Popconfirm, Space, Table, Tag, Typography, Upload } from 'antd';
import { DeleteOutlined, InboxOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, formatDateTime, notifyError, PageContainer, type LibraryDocument, type LibraryPassage } from '@openspoor/ui/pro';
import { useSession } from '../../lib/session';
import { useAction } from '../../lib/hooks';

const KEY = ['/library'] as const;

/** Reads a file as base64 (the API takes the bytes in JSON). */
function toBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).split(',')[1] ?? '');
    r.onerror = () => reject(r.error ?? new Error('read failed'));
    r.readAsDataURL(file);
  });
}

/** Renders a search excerpt, turning [[match]] into highlighted text without any HTML. */
function Excerpt({ text }: { text: string }) {
  const parts = text.split(/(\[\[.*?\]\])/g);
  return (
    <Typography.Paragraph style={{ marginBottom: 4, whiteSpace: 'pre-wrap' }}>
      {parts.map((p, i) => (p.startsWith('[[') && p.endsWith(']]') ? <mark key={i}>{p.slice(2, -2)}</mark> : <span key={i}>{p}</span>))}
    </Typography.Paragraph>
  );
}

/** Document library: upload (text is extracted without OCR), full-text search and removal. */
export default function DocumentLibrary() {
  const { t } = useTranslation();
  const { hasRole } = useSession();
  const canWrite = hasRole('Serialization', 'Compliance', 'Ops', 'Integration');
  const [query, setQuery] = useState('');
  const docs = useQuery({ queryKey: KEY, queryFn: () => api.list<LibraryDocument>('/library') });
  const hits = useQuery({
    queryKey: ['/library/search', query],
    queryFn: () => api.list<LibraryPassage>('/library/search', { q: query }),
    enabled: query.trim().length >= 2,
  });
  const upload = useAction(
    async (file: File) => api.post<LibraryDocument>('/library', { name: file.name, mediaType: file.type, contentBase64: await toBase64(file) }),
    { invalidate: [KEY], success: t('library.uploaded') },
  );
  const remove = useAction((id: string) => api.del(`/library/${id}`), { invalidate: [KEY, ['/library/search']], success: t('library.removed') });
  return (
    <PageContainer title={t('nav.documentLibrary')} subTitle={t('library.subtitle')}>
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Alert type="info" showIcon message={t('library.limits')} />
        {canWrite && (
          <Upload.Dragger
            multiple={false}
            showUploadList={false}
            disabled={upload.isPending}
            beforeUpload={(file) => {
              upload.mutate(file);
              return false;
            }}
          >
            <p className="ant-upload-drag-icon"><InboxOutlined /></p>
            <p className="ant-upload-text">{t('library.drop')}</p>
          </Upload.Dragger>
        )}
        <Card title={t('library.search')}>
          <Input.Search allowClear placeholder={t('library.searchHint')} onSearch={(v) => setQuery(v)} loading={hits.isFetching} />
          {query.trim().length >= 2 && (
            <List
              style={{ marginTop: 12 }}
              loading={hits.isLoading}
              locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('library.noHits')} /> }}
              dataSource={hits.data?.items ?? []}
              renderItem={(h) => (
                <List.Item>
                  <div style={{ width: '100%' }}>
                    <Excerpt text={h.text} />
                    <Space size={8}>
                      <Typography.Text strong>{h.document}</Typography.Text>
                      <Tag>{t('library.cite')}: <Typography.Text code copyable>{h.ref}</Typography.Text></Tag>
                    </Space>
                  </div>
                </List.Item>
              )}
            />
          )}
        </Card>
        <Table<LibraryDocument>
          size="small"
          rowKey="id"
          loading={docs.isLoading}
          dataSource={docs.data?.items ?? []}
          pagination={false}
          columns={[
            { title: t('library.name'), dataIndex: 'name' },
            { title: t('library.format'), dataIndex: 'format', render: (v: string) => <Tag>{v}</Tag> },
            { title: t('library.pages'), dataIndex: 'pages', render: (v: number) => v || '–' },
            { title: t('library.passages'), dataIndex: 'passages' },
            { title: t('library.size'), dataIndex: 'sizeBytes', render: (v: number) => `${Math.max(1, Math.round(v / 1024))} KiB` },
            {
              title: '',
              dataIndex: 'truncated',
              render: (v: boolean) => (v ? <Tag color="warning">{t('library.truncated')}</Tag> : null),
            },
            { title: t('field.createdAt'), dataIndex: 'createdAt', render: (v: string) => formatDateTime(v) },
            {
              title: '',
              key: 'rm',
              render: (_v, r) =>
                canWrite && (
                  <Popconfirm title={`${t('library.remove')}?`} onConfirm={() => remove.mutateAsync(r.id).catch(notifyError)}>
                    <Button size="small" danger icon={<DeleteOutlined />} aria-label={t('library.remove')} />
                  </Popconfirm>
                ),
            },
          ]}
        />
      </Space>
    </PageContainer>
  );
}
