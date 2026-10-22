import { useMemo, useState } from 'react';
import { Card, Col, Empty, Row, Select, Tag, Typography } from 'antd';
import { ReadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, PageContainer, type LearnArticle } from '@openspoor/ui/pro';

export default function Learn() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const [tag, setTag] = useState<string | undefined>();
  const all = useQuery({ queryKey: ['/learn'], queryFn: () => api.list<LearnArticle>('/learn') });
  const tags = useMemo(() => [...new Set((all.data?.items ?? []).flatMap((a) => a.tags ?? []))].sort(), [all.data]);
  const items = (all.data?.items ?? []).filter((a) => !tag || a.tags?.includes(tag));

  return (
    <PageContainer
      title={t('nav.learn')}
      subTitle="Guides to the standards and to Open Spoor"
      extra={<Select allowClear placeholder="Tag" style={{ width: 180 }} value={tag} onChange={setTag} options={tags.map((x) => ({ value: x, label: x }))} />}
    >
      {all.isLoading ? (
        <Card loading />
      ) : !items.length ? (
        <Card>
          <Empty description={t('common.empty')} />
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {items.map((a) => (
            <Col key={a.slug} xs={24} md={12} xl={8}>
              <Card hoverable onClick={() => nav(`/ecosystem/learn/${encodeURIComponent(a.slug)}`)} style={{ height: '100%' }}>
                <Card.Meta
                  avatar={<ReadOutlined style={{ fontSize: 20 }} />}
                  title={<Typography.Text strong>{a.title || a.slug}</Typography.Text>}
                  description={
                    <div>
                      {(a.tags ?? []).map((x) => (
                        <Tag
                          key={x}
                          color={x === tag ? 'blue' : undefined}
                          onClick={(e) => {
                            e.stopPropagation();
                            setTag(x);
                          }}
                          style={{ cursor: 'pointer', marginTop: 4 }}
                        >
                          {x}
                        </Tag>
                      ))}
                    </div>
                  }
                />
              </Card>
            </Col>
          ))}
        </Row>
      )}
    </PageContainer>
  );
}
