import { useRef, useState } from 'react';
import { Button, Card, Flex, Tag, Typography } from 'antd';
import { ClearOutlined, RobotOutlined, UserOutlined } from '@ant-design/icons';
import { Bubble, Prompts, Sender, Welcome, type BubbleItemType } from '@ant-design/x';
import { useTranslation } from 'react-i18next';
import { api, errorMessage, PageContainer, type ChatMessage, type ChatReply } from '@openspoor/ui/pro';

interface Msg extends ChatMessage {
  key: number;
  citations?: ChatReply['citations'];
  error?: boolean;
}

const SUGGESTIONS = [
  'Which serials were decommissioned in the last 24 hours?',
  'Summarise open DSCSA exceptions for my partners.',
  'Why was EU FMD report resubmission needed?',
  'Which supplier certificates expire within 60 days?',
];

function citationLabel(c: NonNullable<ChatReply['citations']>[number]): string {
  return typeof c === 'string' ? c : (c.title ?? c.ref ?? c.url ?? JSON.stringify(c));
}

export default function Assistant() {
  const { t } = useTranslation();
  const [messages, setMessages] = useState<Msg[]>([]);
  const [value, setValue] = useState('');
  const [loading, setLoading] = useState(false);
  const seq = useRef(0);
  const abort = useRef<AbortController | null>(null);

  const send = async (text: string) => {
    const content = text.trim();
    if (!content || loading) return;
    const next: Msg[] = [...messages, { key: ++seq.current, role: 'user', content }];
    setMessages(next);
    setValue('');
    setLoading(true);
    abort.current = new AbortController();
    try {
      const res = await api.post<ChatReply>(
        '/assistant/chat',
        { messages: next.filter((m) => !m.error).map(({ role, content: c }) => ({ role, content: c })) },
        { signal: abort.current.signal },
      );
      setMessages((m) => [...m, { key: ++seq.current, role: 'assistant', content: res?.reply ?? '', citations: res?.citations }]);
    } catch (e) {
      if (!(e instanceof DOMException && e.name === 'AbortError')) {
        setMessages((m) => [...m, { key: ++seq.current, role: 'assistant', content: errorMessage(e).title, error: true }]);
      }
    } finally {
      setLoading(false);
    }
  };

  const items: BubbleItemType[] = messages.map((m) => ({
    key: m.key,
    role: m.role === 'user' ? 'user' : 'ai',
    content: m.content,
    footer: m.citations?.length ? (
      <Flex wrap gap={4}>
        {m.citations.map((c, i) => (
          <Tag key={i} color="blue">
            [{i + 1}] {citationLabel(c)}
          </Tag>
        ))}
      </Flex>
    ) : undefined,
    styles: m.error ? { content: { color: '#C0392B' } } : undefined,
  }));
  if (loading) items.push({ key: 'loading', role: 'ai', content: '', loading: true });

  return (
    <PageContainer
      title={t('nav.assistant')}
      extra={<Button icon={<ClearOutlined />} onClick={() => setMessages([])} disabled={!messages.length}>{t('action.reset')}</Button>}
    >
      <Card styles={{ body: { display: 'flex', flexDirection: 'column', height: 'calc(100vh - 240px)', minHeight: 420 } }}>
        <div style={{ flex: 1, overflow: 'auto', marginBottom: 12 }}>
          {messages.length === 0 ? (
            <Flex vertical gap={16}>
              <Welcome
                icon={<RobotOutlined style={{ fontSize: 40 }} />}
                title="Open Spoor Assistant"
                description={<Typography.Text type="secondary">Ask about your traceability data, compliance status and partner exceptions. Answers cite their sources.</Typography.Text>}
              />
              <Prompts title="Try" items={SUGGESTIONS.map((s, i) => ({ key: String(i), description: s }))} onItemClick={(info) => send(String(info.data.description))} wrap />
            </Flex>
          ) : (
            <Bubble.List
              items={items}
              role={{
                ai: { placement: 'start', avatar: <RobotOutlined />, variant: 'outlined', typing: { effect: 'fade-in' } },
                user: { placement: 'end', avatar: <UserOutlined />, variant: 'filled' },
              }}
              style={{ height: '100%' }}
            />
          )}
        </div>
        <Sender
          value={value}
          onChange={(v) => setValue(v)}
          onSubmit={send}
          loading={loading}
          onCancel={() => abort.current?.abort()}
          placeholder={`${t('action.send')}…`}
        />
      </Card>
    </PageContainer>
  );
}
