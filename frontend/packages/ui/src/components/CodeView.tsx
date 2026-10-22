import { Button, Flex, Typography, theme } from 'antd';
import { CopyOutlined, DownloadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { copyText, downloadBlob } from '../files';
import { notifySuccess } from '../notify';

export interface CodeViewProps {
  code: string;
  filename?: string;
  mime?: string;
  maxHeight?: number | string;
  title?: React.ReactNode;
}

/** Read-only code block with copy and download actions (no syntax-highlight dependency). */
export function CodeView({ code, filename, mime, maxHeight = 480, title }: CodeViewProps) {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  return (
    <div>
      <Flex justify="space-between" align="center" gap={8} style={{ marginBottom: 8 }}>
        <Typography.Text strong>{title}</Typography.Text>
        <Flex gap={8}>
          <Button size="small" icon={<CopyOutlined />} onClick={async () => (await copyText(code)) && notifySuccess(t('action.copied'))}>
            {t('action.copy')}
          </Button>
          {filename && (
            <Button size="small" icon={<DownloadOutlined />} onClick={() => downloadBlob(code, filename, mime)}>
              {t('action.download')}
            </Button>
          )}
        </Flex>
      </Flex>
      <pre
        dir="ltr"
        style={{
          margin: 0,
          padding: 12,
          maxHeight,
          overflow: 'auto',
          fontFamily: token.fontFamilyCode,
          fontSize: 12,
          background: token.colorFillTertiary,
          border: `1px solid ${token.colorBorderSecondary}`,
          borderRadius: token.borderRadius,
          whiteSpace: 'pre',
        }}
      >
        {code}
      </pre>
    </div>
  );
}
