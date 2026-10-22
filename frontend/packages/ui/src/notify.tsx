import type { NotificationInstance } from 'antd/es/notification/interface';
import type { MessageInstance } from 'antd/es/message/interface';
import { errorMessage } from './api/errors';

let notificationApi: NotificationInstance | null = null;
let messageApi: MessageInstance | null = null;

/** Called once by AppProvider with the context-aware antd APIs. */
export function bindNotifier(n: NotificationInstance, m: MessageInstance): void {
  notificationApi = n;
  messageApi = m;
}

export function notifyError(err: unknown, fallbackTitle?: string): void {
  const { title, description } = errorMessage(err);
  if (notificationApi) {
    notificationApi.error({ title: title || fallbackTitle, description: description ? <pre style={{ whiteSpace: 'pre-wrap', margin: 0, fontFamily: 'inherit' }}>{description}</pre> : undefined });
  } else {
    console.error(title, description);
  }
}

export function notifySuccess(text: string): void {
  messageApi?.success(text);
}

export function notifyInfo(text: string): void {
  messageApi?.info(text);
}
