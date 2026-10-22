import { createContext, useContext, useEffect, useMemo, useSyncExternalStore, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, tokenStore, useAppSettings, type Principal, type Tenant, type TokenResponse } from '@openspoor/ui/pro';

interface Session {
  token: string | null;
  principal?: Principal;
  tenant?: Tenant;
  loading: boolean;
  hasRole: (...roles: string[]) => boolean;
  isFood: boolean;
  signIn: (res: TokenResponse) => void;
  signOut: () => void;
}

const Ctx = createContext<Session | null>(null);

export function useSession(): Session {
  const s = useContext(Ctx);
  if (!s) throw new Error('useSession outside SessionProvider');
  return s;
}

export function useToken(): string | null {
  return useSyncExternalStore(
    (cb) => tokenStore.subscribe(cb),
    () => tokenStore.get(),
  );
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const token = useToken();
  const qc = useQueryClient();
  const { setBrandColor } = useAppSettings();

  const me = useQuery({ queryKey: ['me', token], queryFn: () => api.get<Principal>('/me'), enabled: !!token, staleTime: 60_000 });
  const tenant = useQuery({ queryKey: ['tenant', token], queryFn: () => api.get<Tenant>('/tenant'), enabled: !!token, staleTime: 60_000 });

  const brand = tenant.data?.brand?.primaryColor;
  useEffect(() => {
    setBrandColor(token && typeof brand === 'string' ? brand : null);
  }, [brand, token, setBrandColor]);

  const value = useMemo<Session>(() => {
    const roles = me.data?.roles ?? [];
    return {
      token,
      principal: me.data,
      tenant: tenant.data,
      loading: !!token && (me.isLoading || tenant.isLoading),
      hasRole: (...r) => roles.includes('Admin') || r.some((x) => roles.includes(x)),
      isFood: (tenant.data?.verticals ?? []).includes('food'),
      signIn: (res) => {
        qc.clear();
        tokenStore.set(res.token);
      },
      signOut: () => {
        tokenStore.set(null);
        qc.clear();
      },
    };
  }, [token, me.data, me.isLoading, tenant.data, tenant.isLoading, qc]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
