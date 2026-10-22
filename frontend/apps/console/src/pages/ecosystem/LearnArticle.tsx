import { Card, Empty, Tag } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, PageContainer, type LearnArticle as Article } from '@openspoor/ui/pro';
import { Markdown } from '../../lib/markdown';

export default function LearnArticle() {
  const { t } = useTranslation();
  const { slug = '' } = useParams();
  const nav = useNavigate();
  const q = useQuery({ queryKey: ['/learn', slug], queryFn: () => api.get<Article>(`/learn/${encodeURIComponent(slug)}`), enabled: !!slug });
  const a = q.data;
  return (
    <PageContainer
      title={a?.title ?? slug}
      onBack={() => nav('/ecosystem/learn')}
      tags={(a?.tags ?? []).map((x) => (
        <Tag key={x}>{x}</Tag>
      ))}
      loading={q.isLoading}
    >
      <Card style={{ maxWidth: 880 }}>{a?.body ? <Markdown source={a.body} /> : <Empty description={q.isError ? t('common.error') : t('common.empty')} />}</Card>
    </PageContainer>
  );
}
