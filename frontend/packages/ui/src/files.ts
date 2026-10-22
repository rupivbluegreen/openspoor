/** Triggers a browser download of text or a Blob. */
export function downloadBlob(data: Blob | string, filename: string, type = 'text/plain;charset=utf-8'): void {
  const blob = typeof data === 'string' ? new Blob([data], { type }) : data;
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export function readFileText(file: Blob): Promise<string> {
  return file.text();
}

/** Guesses the content type of an EPCIS document from its text. */
export function epcisContentType(text: string): 'application/json' | 'application/xml' {
  return text.trimStart().startsWith('<') ? 'application/xml' : 'application/json';
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

export function formatDateTime(v?: string | null): string {
  if (!v) return '–';
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? v : d.toLocaleString();
}

export function prettyJson(v: unknown): string {
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2);
    } catch {
      return v;
    }
  }
  return JSON.stringify(v, null, 2) ?? '';
}
