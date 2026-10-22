import { useRef, useState } from 'react';
import { Alert, Button, Card, Col, Empty, Flex, Input, Row, Segmented, Space, Statistic, Tabs, Timeline, Typography, Upload } from 'antd';
import { ApartmentOutlined, CheckCircleOutlined, ExperimentOutlined, SwapOutlined, UploadOutlined } from '@ant-design/icons';
import { useMutation } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, EventErrors, formatDateTime, notifyError, SvgGraph, type GraphEdge, type PlaygroundValidate, type PlaygroundVisualize,
} from '@openspoor/ui';
import { SAMPLE_EPCIS } from './sample';

type Target = 'json' | 'xml';

export function toGraph(v?: PlaygroundVisualize) {
  const nodes = (v?.nodes ?? []).map((n) => ({ id: String(n.id), label: n.label, kind: n.kind ?? n.type }));
  const edges: GraphEdge[] = (v?.edges ?? [])
    .map((e) => ({ from: String(e.from ?? e.source ?? ''), to: String(e.to ?? e.target ?? ''), label: e.label }))
    .filter((e) => e.from && e.to);
  return { nodes, edges };
}

export default function Playground() {
  const { t } = useTranslation();
  const [doc, setDoc] = useState('');
  const [target, setTarget] = useState<Target>('xml');
  const [tab, setTab] = useState('validate');
  const results = useRef<HTMLDivElement>(null);
  const opts = { anonymous: true } as const;

  const validate = useMutation({ mutationFn: () => api.post<PlaygroundValidate>('/playground/validate', { document: doc }, opts), onError: notifyError, meta: { silent: true } });
  const convert = useMutation({ mutationFn: () => api.post<{ document: string }>('/playground/convert', { document: doc, to: target }, opts), onError: notifyError, meta: { silent: true } });
  const visualize = useMutation({ mutationFn: () => api.post<PlaygroundVisualize>('/playground/visualize', { document: doc }, opts), onError: notifyError, meta: { silent: true } });

  const run = (k: 'validate' | 'convert' | 'visualize') => {
    setTab(k);
    setTimeout(() => results.current?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 50);
    ({ validate, convert, visualize })[k].mutate();
  };
  const v = validate.data;
  const converted = convert.data?.document;
  const graph = toGraph(visualize.data);

  return (
    <Flex vertical gap={16} style={{ maxWidth: 1200, margin: '0 auto' }}>
      <div>
        <Typography.Title level={3} style={{ marginBottom: 0 }}>
          <ExperimentOutlined /> {t('playground.title')}
        </Typography.Title>
        <Typography.Text type="secondary">{t('playground.subtitle')}</Typography.Text>
      </div>
      <Card
        title={t('playground.input')}
        extra={
          <Space wrap>
            <Button onClick={() => setDoc(SAMPLE_EPCIS)}>{t('action.loadSample')}</Button>
            <Upload
              accept=".json,.jsonld,.xml"
              showUploadList={false}
              beforeUpload={async (f) => {
                setDoc(await f.text());
                return false;
              }}
            >
              <Button icon={<UploadOutlined />}>{t('action.upload')}</Button>
            </Upload>
          </Space>
        }
      >
        <Input.TextArea
          dir="ltr"
          value={doc}
          onChange={(e) => setDoc(e.target.value)}
          autoSize={{ minRows: 8, maxRows: 14 }}
          placeholder='{"@context": ["https://ref.gs1.org/standards/epcis/2.0.0/epcis-context.jsonld"], "type": "EPCISDocument", …}  or  <epcis:EPCISDocument …>'
          style={{ fontFamily: 'monospace', fontSize: 12 }}
        />
        <Flex wrap gap={8} style={{ marginTop: 12 }}>
          <Button type="primary" icon={<CheckCircleOutlined />} disabled={!doc.trim()} loading={validate.isPending} onClick={() => run('validate')}>
            {t('action.validate')}
          </Button>
          <Space.Compact>
            <Button icon={<SwapOutlined />} disabled={!doc.trim()} loading={convert.isPending} onClick={() => run('convert')}>
              {t('action.convert')} → {target.toUpperCase()}
            </Button>
            <Segmented value={target} onChange={(x) => setTarget(x as Target)} options={[{ value: 'xml', label: 'XML' }, { value: 'json', label: 'JSON-LD' }]} />
          </Space.Compact>
          <Button icon={<ApartmentOutlined />} disabled={!doc.trim()} loading={visualize.isPending} onClick={() => run('visualize')}>
            {t('action.visualize')}
          </Button>
        </Flex>
      </Card>
      <Card ref={results}>
        <Tabs
          activeKey={tab}
          onChange={setTab}
          items={[
            {
              key: 'validate',
              label: t('action.validate'),
              children: v ? (
                <Flex vertical gap={12}>
                  <Alert type={v.valid ? 'success' : 'error'} showIcon title={v.valid ? t('playground.valid') : t('playground.invalid')} />
                  <Row gutter={16}>
                    <Col xs={8}><Statistic title={t('playground.format')} value={v.format ?? '–'} /></Col>
                    <Col xs={8}><Statistic title={t('playground.events')} value={v.eventCount ?? 0} /></Col>
                    <Col xs={8}><Statistic title={t('playground.errors')} value={v.errors?.length ?? 0} /></Col>
                  </Row>
                  {!!v.errors?.length && <EventErrors errors={v.errors} />}
                </Flex>
              ) : (
                <Empty />
              ),
            },
            {
              key: 'convert',
              label: t('action.convert'),
              children: converted ? (
                <CodeView code={converted} filename={`epcis.${target === 'xml' ? 'xml' : 'jsonld'}`} mime={target === 'xml' ? 'application/xml' : 'application/ld+json'} maxHeight="60vh" />
              ) : (
                <Empty />
              ),
            },
            {
              key: 'visualize',
              label: t('action.visualize'),
              children: visualize.data ? (
                <Row gutter={[16, 16]}>
                  <Col xs={24} xl={15}>
                    <Typography.Title level={5}>{t('playground.graph')}</Typography.Title>
                    <SvgGraph nodes={graph.nodes} edges={graph.edges} emptyText={t('common.empty')} />
                  </Col>
                  <Col xs={24} xl={9}>
                    <Typography.Title level={5}>{t('playground.timeline')}</Typography.Title>
                    {visualize.data.timeline?.length ? (
                      <Timeline
                        items={visualize.data.timeline.map((e, i) => ({
                          key: i,
                          title: formatDateTime(String(e.time ?? e.eventTime ?? '')),
                          content: `${e.label ?? e.bizStep ?? ''}${e.type ? ` · ${e.type}` : ''}`,
                        }))}
                      />
                    ) : (
                      <Empty />
                    )}
                  </Col>
                </Row>
              ) : (
                <Empty />
              ),
            },
          ]}
        />
      </Card>
    </Flex>
  );
}
