import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, type Organization, type Partner, type Product, type Site } from '@openspoor/ui/pro';
import type { TransactionType, TransactionTypeList } from '../pages/link/linkTypes';

/** Select options loaded from list endpoints (shared cache keys with the list pages). */
function useOptions<T extends Record<string, unknown>>(path: string, label: (x: T) => string, value: (x: T) => string = (x) => String(x.id)) {
  const q = useQuery({ queryKey: [path], queryFn: () => api.list<T>(path), meta: { silent: true } });
  return (q.data?.items ?? []).map((x) => ({ value: value(x), label: label(x) }));
}

export const useOrgOptions = () => useOptions<Organization & Record<string, unknown>>('/orgs', (o) => o.name);
export const useSiteOptions = () => useOptions<Site & Record<string, unknown>>('/sites', (s) => `${s.name} (${s.gln})`);
export const useProductOptions = () => useOptions<Product & Record<string, unknown>>('/products', (p) => `${p.name} · ${p.gtin}`);
export const usePartnerOptions = () => useOptions<Partner & Record<string, unknown>>('/partners', (p) => p.name);

export const MARKETS = ['US', 'EU', 'SA', 'GB', 'CH', 'AE', 'BR', 'IN', 'CN'].map((m) => ({ value: m, label: m }));

export const usePlotOptions = () =>
  useOptions<{ id?: string; name: string } & Record<string, unknown>>('/food/plots', (p) => p.name, (p) => String(p.id ?? p.name));
export const UOMS = ['kg', 'lb', 't', 'case', 'each', 'L'].map((u) => ({ value: u, label: u }));
export const CTE_TYPES = ['harvesting', 'cooling', 'initial_packing', 'first_land_based_receiving', 'shipping', 'receiving', 'transformation'];

/** B2B document types from the registry (GET /b2b/types), grouped by category; falls back to the original six. */
export function useDocTypeOptions(withEpcis = false) {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ['/b2b/types'], queryFn: () => api.get<TransactionTypeList>('/b2b/types'), staleTime: 5 * 60_000, meta: { silent: true } });
  const items = q.data?.items ?? ['order', 'asn', 'invoice', 'inventory', 'return', 'chargeback'].map((key) => ({ key, name: key, category: 'ordering', standards: {} }) as TransactionType);
  const label = (x: TransactionType) => {
    const std = [x.standards.x12 && `X12 ${x.standards.x12}`, x.standards.edifact].filter(Boolean).join(' · ');
    return std ? `${x.name} (${std})` : x.name;
  };
  const groups = new Map<string, { value: string; label: string }[]>();
  for (const x of items) groups.set(x.category, [...(groups.get(x.category) ?? []), { value: x.key, label: label(x) }]);
  const grouped: { label: string; options: { value: string; label: string }[] }[] = [...groups].map(([cat, options]) => ({
    label: t(`b2b.category.${cat}`, { defaultValue: cat }),
    options,
  }));
  if (withEpcis) grouped.push({ label: 'EPCIS', options: [{ value: 'epcis', label: 'EPCIS events' }] });
  return { grouped, flat: items.map((x) => ({ value: x.key, label: x.name })) };
}
