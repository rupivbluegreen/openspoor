import { Card, Empty, Space } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, EventErrors, PageContainer, ProDescriptions, StatusTag, type CaptureJob } from '@openspoor/ui/pro';


const RUNNING = new Set(['pending', 'queued', 'processing', 'running', 'accepted']);

export default function CaptureDetail() {
  const { id = '' } = useParams();
  const { t } = useTranslation();
  const nav = useNavigate();
  const q = useQuery({
    queryKey: ['/epcis/capture', id],
    queryFn: () => api.get<CaptureJob>(`/epcis/capture/${encodeURIComponent(id)}`),
    refetchInterval: (query) => (RUNNING.has(String(query.state.data?.status ?? '').toLowerCase()) ? 2000 : false),
  });
  return (
    <PageContainer title={`Capture ${id}`} onBack={() => nav('/trace/epcis')} loading={q.isLoading}>
      {q.data ? (
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <Card>
            <ProDescriptions<CaptureJob>
              column={{ xs: 1, md: 3 }}
              dataSource={q.data}
              columns={[
                { title: t('field.status'), dataIndex: 'status', render: (_d, r) => <StatusTag status={r.status} /> },
                { title: t('field.events'), dataIndex: 'eventCount' },
                { title: t('playground.errors'), dataIndex: 'errorCount', render: (_d, r) => r.errorCount ?? r.errors?.length ?? 0 },
                { title: t('field.createdAt'), dataIndex: 'createdAt', valueType: 'dateTime' },
                { title: 'Finished', dataIndex: 'finishedAt', valueType: 'dateTime' },
              ]}
            />
          </Card>
          <Card title={t('playground.errors')}>
            <EventErrors errors={q.data.errors} />
          </Card>
        </Space>
      ) : (
        !q.isLoading && <Empty />
      )}
    </PageContainer>
  );
}
