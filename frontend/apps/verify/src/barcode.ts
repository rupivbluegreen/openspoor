/* Minimal typings for the Shape Detection API (not yet in lib.dom). */
export interface DetectedBarcode {
  rawValue: string;
  format: string;
}
interface BarcodeDetectorInstance {
  detect(source: CanvasImageSource): Promise<DetectedBarcode[]>;
}
interface BarcodeDetectorCtor {
  new (opts?: { formats?: string[] }): BarcodeDetectorInstance;
  getSupportedFormats?: () => Promise<string[]>;
}

const WANTED = ['data_matrix', 'qr_code'];

/** Returns a detector for DataMatrix / QR when the browser supports it, else null. */
export async function createDetector(): Promise<BarcodeDetectorInstance | null> {
  const Ctor = (globalThis as unknown as { BarcodeDetector?: BarcodeDetectorCtor }).BarcodeDetector;
  if (!Ctor) return null;
  try {
    const supported = (await Ctor.getSupportedFormats?.()) ?? WANTED;
    const formats = WANTED.filter((f) => supported.includes(f));
    if (!formats.length) return null;
    return new Ctor({ formats });
  } catch {
    return null;
  }
}

export function cameraAvailable(): boolean {
  return typeof navigator !== 'undefined' && !!navigator.mediaDevices?.getUserMedia && 'BarcodeDetector' in globalThis;
}
