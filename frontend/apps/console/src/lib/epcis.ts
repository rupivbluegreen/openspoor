import type { EpcisEvent } from '@openspoor/ui/pro';

/** Last segment of a CBV URI ("urn:epcglobal:cbv:bizstep:shipping" → "shipping"). */
export function cbv(v?: unknown): string {
  if (typeof v !== 'string' || !v) return '–';
  const s = v.split(/[:/#]/).filter(Boolean);
  return s[s.length - 1] ?? v;
}

export function locationId(v: EpcisEvent['bizLocation']): string | undefined {
  if (!v) return undefined;
  return typeof v === 'string' ? v : v.id;
}

/** Pulls the event list out of an EPCISQueryDocument (2.0 JSON) or an EPCISDocument. */
export function extractEvents(doc: unknown): EpcisEvent[] {
  if (!doc || typeof doc !== 'object') return [];
  const d = doc as Record<string, any>;
  const candidates = [
    d.epcisBody?.queryResults?.resultsBody?.eventList,
    d.epcisBody?.eventList,
    d.queryResults?.resultsBody?.eventList,
    d.eventList,
    d.items,
    Array.isArray(doc) ? doc : undefined,
  ];
  for (const c of candidates) if (Array.isArray(c)) return c as EpcisEvent[];
  return [];
}

export function nextPageToken(doc: unknown): string | undefined {
  if (!doc || typeof doc !== 'object') return undefined;
  const d = doc as Record<string, any>;
  return d.nextPageToken ?? d.epcisBody?.queryResults?.nextPageToken ?? undefined;
}

export const EVENT_TYPES = ['ObjectEvent', 'AggregationEvent', 'TransactionEvent', 'TransformationEvent', 'AssociationEvent'];
export const BIZ_STEPS = [
  'commissioning', 'packing', 'unpacking', 'shipping', 'receiving', 'dispensing', 'decommissioning', 'destroying',
  'inspecting', 'storing', 'transforming', 'holding', 'sampling', 'retail_selling',
];
export const DISPOSITIONS = ['active', 'in_transit', 'in_progress', 'dispensed', 'destroyed', 'expired', 'recalled', 'stolen', 'damaged', 'sellable_accessible', 'non_sellable_other'];
