import { useState } from 'react';
import { Alert, Button, Modal, Upload } from 'antd';
import { EditOutlined, UploadOutlined } from '@ant-design/icons';
import { useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, ApiError, EventErrors, epcisContentType, ModalForm, notifyError, PageContainer, ProFormTextArea, QueryTable, StatusTag, type CaptureJob } from '@openspoor/ui/pro';


type Row = CaptureJob & Record<string, unknown>;

export function useCapture() {
  const qc = useQueryClient();
  const [result, setResult] = useState<{ ok: boolean; job?: CaptureJob; title?: string; errors?: unknown[] } | null>(null);
  const capture = async (text: string) => {
    try {
      const job = await api.request<CaptureJob>('POST', '/epcis/capture', { body: text, headers: { 'Content-Type': epcisContentType(text) } });
      setResult({ ok: true, job });
      await qc.invalidateQueries({ queryKey: ['/epcis/capture'] });
      return true;
    } catch (e) {
      if (e instanceof ApiError && e.status === 422) setResult({ ok: false, title: e.title, errors: e.errors });
      else notifyError(e);
      return false;
    }
  };
  return { capture, result, clear: () => setResult(null) };
}

export default function EpcisInbox() {
  const { t } = useTranslation();
  const { capture, result, clear } = useCapture();
  const [busy, setBusy] = useState(false);

  return (
    <PageContainer title={t('nav.epcisInbox')} subTitle="EPCIS 2.0 JSON-LD / XML (1.2 accepted)">
      <QueryTable<Row>
        rowKey={(r) => String(r.id ?? r.captureId)}
        queryKey={['/epcis/capture']}
        tableId="epcis-inbox"
        fetcher={() => api.list<Row>('/epcis/capture')}
        columns={[
          { title: t('field.id'), dataIndex: 'id', render: (_d, r) => <Link to={`/trace/epcis/${r.id ?? r.captureId}`}>{String(r.id ?? r.captureId)}</Link> },
          { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
          { title: t('field.events'), dataIndex: 'eventCount', valueType: 'digit' },
          { title: t('playground.errors'), dataIndex: 'errorCount', render: (_d, r) => r.errorCount ?? r.errors?.length ?? 0 },
          { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
          { title: 'Finished', dataIndex: 'finishedAt', valueType: 'dateTime' },
        ]}
        toolBarRender={() => [
          <ModalForm<{ document: string }>
            key="paste"
            title={t('playground.input')}
            width={720}
            trigger={<Button icon={<EditOutlined />}>Paste</Button>}
            modalProps={{ destroyOnHidden: true }}
            onFinish={(v) => capture(v.document)}
          >
            <ProFormTextArea name="document" rules={[{ required: true }]} fieldProps={{ rows: 16, style: { fontFamily: 'monospace' } }} />
          </ModalForm>,
          <Upload
            key="upload"
            accept=".json,.jsonld,.xml,application/json,application/ld+json,application/xml,text/xml"
            showUploadList={false}
            beforeUpload={async (file) => {
              setBusy(true);
              await capture(await file.text());
              setBusy(false);
              return false;
            }}
          >
            <Button type="primary" icon={<UploadOutlined />} loading={busy}>
              {t('action.upload')}
            </Button>
          </Upload>,
        ]}
      />
      <Modal open={!!result} onCancel={clear} footer={null} width={760} title={t('common.result')}>
        {result?.ok ? (
          <Alert
            type={result.job?.errors?.length ? 'warning' : 'success'}
            showIcon
            title={`Capture ${result.job?.captureId ?? result.job?.id ?? ''} · ${result.job?.status ?? ''}`}
            description={
              <>
                {result.job?.eventCount ?? 0} events.{' '}
                {(result.job?.captureId ?? result.job?.id) && <Link to={`/trace/epcis/${result.job?.captureId ?? result.job?.id}`}>{t('action.view')}</Link>}
              </>
            }
          />
        ) : (
          <Alert type="error" showIcon title={result?.title} />
        )}
        {(result?.job?.errors?.length || result?.errors?.length) ? (
          <div style={{ marginTop: 12 }}>
            <EventErrors errors={result?.job?.errors ?? result?.errors} />
          </div>
        ) : null}
      </Modal>
    </PageContainer>
  );
}
