import { useCallback } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { notifySuccess } from '@openspoor/ui/pro';
import { useTranslation } from 'react-i18next';

/**
 * Wraps an API call in a TanStack mutation for ProForm/ModalForm `onFinish`:
 * resolves `true` on success (closes the modal), `false` on error (the MutationCache
 * shows the problem+json title as a notification). Invalidates the given query keys.
 */
export function useSubmit<V, R = unknown>(
  fn: (v: V) => Promise<R>,
  opts: { invalidate?: readonly (readonly unknown[])[]; success?: string | false; onSuccess?: (r: R, v: V) => void } = {},
) {
  const qc = useQueryClient();
  const { t } = useTranslation();
  const m = useMutation({ mutationFn: fn });
  const { invalidate, success, onSuccess } = opts;
  return useCallback(
    async (v: V): Promise<boolean> => {
      try {
        const r = await m.mutateAsync(v);
        await Promise.all((invalidate ?? []).map((k) => qc.invalidateQueries({ queryKey: k })));
        if (success !== false) notifySuccess(success ?? t('common.success'));
        onSuccess?.(r, v);
        return true;
      } catch {
        return false;
      }
    },
    [m, qc, invalidate, success, onSuccess, t],
  );
}

/** Mutation for buttons (approve, resolve…): returns the mutation object. */
export function useAction<V, R = unknown>(
  fn: (v: V) => Promise<R>,
  opts: { invalidate?: readonly (readonly unknown[])[]; success?: string | false; onSuccess?: (r: R, v: V) => void } = {},
) {
  const qc = useQueryClient();
  const { t } = useTranslation();
  return useMutation({
    mutationFn: fn,
    onSuccess: async (r, v) => {
      await Promise.all((opts.invalidate ?? []).map((k) => qc.invalidateQueries({ queryKey: k })));
      if (opts.success !== false) notifySuccess(opts.success ?? t('common.success'));
      opts.onSuccess?.(r, v);
    },
  });
}
