import type { ReactNode } from 'react';
import { Table, Typography } from 'antd';
import { CodeView } from '@openspoor/ui/pro';

/**
 * Minimal Markdown → antd Typography renderer for trusted, server-shipped articles.
 * Supports headings, paragraphs, bullet/numbered lists, pipe tables, fenced code,
 * block quotes, horizontal rules and inline **bold**, *italic*, `code` and [links](url).
 * Never uses dangerouslySetInnerHTML.
 */

const INLINE = /(`[^`]+`|\*\*[^*]+\*\*|\*[^*\s][^*]*\*|_[^_\s][^_]*_|\[[^\]]+\]\([^)\s]+\))/g;

function safeHref(href: string): string | undefined {
  return /^(https?:|mailto:|\/|#)/i.test(href) ? href : undefined;
}

export function renderInline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let i = 0;
  for (const m of text.matchAll(INLINE)) {
    const tok = m[0];
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    const k = i++;
    if (tok.startsWith('`')) out.push(<Typography.Text key={k} code>{tok.slice(1, -1)}</Typography.Text>);
    else if (tok.startsWith('**')) out.push(<strong key={k}>{renderInline(tok.slice(2, -2))}</strong>);
    else if (tok.startsWith('[')) {
      const lm = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(tok)!;
      const href = safeHref(lm[2]);
      out.push(
        href ? (
          <Typography.Link key={k} href={href} target={href.startsWith('http') ? '_blank' : undefined} rel="noreferrer">
            {renderInline(lm[1])}
          </Typography.Link>
        ) : (
          lm[1]
        ),
      );
    } else out.push(<em key={k}>{renderInline(tok.slice(1, -1))}</em>);
    last = at + tok.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

const splitRow = (line: string) =>
  line
    .trim()
    .replace(/^\|/, '')
    .replace(/\|$/, '')
    .split('|')
    .map((c) => c.trim());

export function Markdown({ source }: { source: string }) {
  const lines = source.replace(/\r\n?/g, '\n').split('\n');
  const blocks: ReactNode[] = [];
  let i = 0;
  const isTableSep = (l?: string) => !!l && /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(l);
  const startsBlock = (l: string) => /^(#{1,6}\s|```|>\s?|\s*[-*+]\s|\s*\d+[.)]\s|---+\s*$|\*\*\*+\s*$)/.test(l) || (l.includes('|') && isTableSep(lines[i + 1]));

  while (i < lines.length) {
    const line = lines[i];
    const k = blocks.length;
    if (!line.trim()) {
      i++;
      continue;
    }
    if (line.startsWith('```')) {
      const lang = line.slice(3).trim();
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i].startsWith('```')) body.push(lines[i++]);
      i++;
      blocks.push(
        <div key={k} style={{ marginBottom: 16 }} data-lang={lang || undefined}>
          <CodeView code={body.join('\n')} />
        </div>,
      );
      continue;
    }
    const h = /^(#{1,6})\s+(.*)$/.exec(line);
    if (h) {
      const level = Math.min(h[1].length + 1, 5) as 2 | 3 | 4 | 5;
      blocks.push(
        <Typography.Title key={k} level={level}>
          {renderInline(h[2])}
        </Typography.Title>,
      );
      i++;
      continue;
    }
    if (/^(---+|\*\*\*+)\s*$/.test(line)) {
      blocks.push(<hr key={k} style={{ border: 0, borderTop: '1px solid var(--ant-color-split)', margin: '16px 0' }} />);
      i++;
      continue;
    }
    if (line.includes('|') && isTableSep(lines[i + 1])) {
      const head = splitRow(line);
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && lines[i].includes('|') && lines[i].trim()) rows.push(splitRow(lines[i++]));
      blocks.push(
        <Table
          key={k}
          size="small"
          bordered
          pagination={false}
          style={{ marginBottom: 16 }}
          rowKey="__k"
          dataSource={rows.map((r, ri) => ({ __k: ri, ...Object.fromEntries(r.map((c, ci) => [String(ci), c])) }))}
          columns={head.map((c, ci) => ({ title: renderInline(c), dataIndex: String(ci), render: (v?: string) => renderInline(v ?? '') }))}
        />,
      );
      continue;
    }
    if (/^>\s?/.test(line)) {
      const body: string[] = [];
      while (i < lines.length && /^>\s?/.test(lines[i])) body.push(lines[i++].replace(/^>\s?/, ''));
      blocks.push(
        <blockquote key={k} style={{ margin: '0 0 16px', paddingInlineStart: 12, borderInlineStart: '3px solid var(--ant-color-border)' }}>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            {renderInline(body.join(' '))}
          </Typography.Paragraph>
        </blockquote>,
      );
      continue;
    }
    const li = /^\s*([-*+]|\d+[.)])\s+/;
    if (li.test(line)) {
      const ordered = /^\s*\d/.test(line);
      const items: string[] = [];
      while (i < lines.length && (li.test(lines[i]) || (lines[i].startsWith('  ') && lines[i].trim() && items.length))) {
        if (li.test(lines[i])) items.push(lines[i].replace(li, ''));
        else items[items.length - 1] += ' ' + lines[i].trim();
        i++;
      }
      const List = ordered ? 'ol' : 'ul';
      blocks.push(
        <Typography.Paragraph key={k}>
          <List>
            {items.map((it, j) => (
              <li key={j}>{renderInline(it)}</li>
            ))}
          </List>
        </Typography.Paragraph>,
      );
      continue;
    }
    const para: string[] = [];
    while (i < lines.length && lines[i].trim() && !(para.length && startsBlock(lines[i]))) para.push(lines[i++].trim());
    blocks.push(<Typography.Paragraph key={k}>{renderInline(para.join(' '))}</Typography.Paragraph>);
  }
  return <Typography>{blocks}</Typography>;
}
