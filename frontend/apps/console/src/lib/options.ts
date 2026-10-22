import { useQuery } from '@tanstack/react-query';
import { api, type Organization, type Partner, type Product, type Site } from '@openspoor/ui/pro';

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
