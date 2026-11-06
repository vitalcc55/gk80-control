import { useCallback, useLayoutEffect, useRef, useState } from 'react';

type Intent = { after: number; manual: boolean; returnToStart: boolean; retry: boolean };
type Published<T> = { page: T; after: number; version: string };
type Flight = { generation: number; intent: Intent; version: string };
type Model<T> = {
  id: string;
  version: string;
  available: boolean;
  visible: boolean;
  open: boolean;
  closing: boolean;
  generation: number;
  published: Published<T> | null;
  intent: Intent | null;
  flight: Flight | null;
  error: string;
  scrollVersion: number;
};

/** A disclosure owns one published page, one intent and one physical read. */
export function useRecordPage<T>({
  id,
  version,
  available,
  visible,
  kind,
  read,
}: {
  id: string;
  version: string;
  available: boolean;
  visible: boolean;
  kind: 'measurements' | 'audit';
  read: (id: string, after: number) => Promise<T>;
}) {
  const model = useRef<Model<T>>({
    id,
    version,
    available,
    visible,
    open: false,
    closing: false,
    generation: 0,
    published: null,
    intent: null,
    flight: null,
    error: '',
    scrollVersion: 0,
  });
  const readRef = useRef(read);
  const mounted = useRef(false);
  const [, render] = useState(0);
  const publish = useCallback(() => {
    if (mounted.current) render((value) => value + 1);
  }, []);
  const drain: () => void = useCallback(() => {
    const state = model.current;
    const intent = state.intent;
    if (
      !mounted.current ||
      !state.open ||
      state.closing ||
      !state.visible ||
      !state.available ||
      state.flight ||
      !intent ||
      intent.retry
    )
      return;
    const flight: Flight = { generation: ++state.generation, intent, version: state.version };
    state.flight = flight;
    state.intent = null;
    state.error = '';
    const request = readRef.current;
    const requestId = state.id;
    const canPublish = () =>
      mounted.current &&
      state.generation === flight.generation &&
      state.id === requestId &&
      state.open &&
      !state.closing &&
      state.visible &&
      state.available;
    publish();
    void (async () => {
      try {
        const page = await request(requestId, intent.after);
        if (canPublish()) {
          state.published = { page, after: intent.after, version: flight.version };
          if (intent.returnToStart) state.scrollVersion = flight.generation;
          state.error = '';
        }
      } catch {
        if (canPublish()) {
          state.error =
            kind === 'measurements'
              ? 'Не удалось прочитать измерения.'
              : 'Не удалось прочитать журнал.';
          state.intent = { ...intent, retry: true };
        }
      } finally {
        if (state.flight === flight) state.flight = null;
        if (mounted.current) {
          publish();
          drain();
        }
      }
    })();
  }, [kind, publish]);

  useLayoutEffect(() => {
    mounted.current = true;
    drain();
    return () => {
      mounted.current = false;
      model.current.generation++;
    };
  }, [drain]);

  useLayoutEffect(() => {
    const state = model.current;
    readRef.current = read;
    const changedId = state.id !== id;
    const changedVersion = state.version !== version;
    const hidden = state.visible && !visible;
    const disconnected = state.available && !available;
    if (changedId || changedVersion || hidden || disconnected) {
      const active =
        state.flight?.generation === state.generation ? state.flight.intent : undefined;
      state.generation++;
      if (changedId) {
        state.published = null;
        state.intent = { after: 0, manual: false, returnToStart: false, retry: false };
        state.error = '';
      } else if (hidden) {
        state.intent =
          kind === 'audit'
            ? (state.intent ?? (active ? { ...active, retry: false } : null))
            : active
              ? {
                  after: state.published?.after ?? 0,
                  manual: false,
                  returnToStart: false,
                  retry: false,
                }
              : null;
        if (kind === 'measurements') state.error = '';
      } else if (disconnected && !active && !changedVersion) {
        // No read was interrupted; retain the current intent and only invalidate pending display effects.
      } else if (kind === 'measurements' && state.open && !state.closing && active?.manual) {
        state.intent = { ...active, retry: true };
        state.error = disconnected
          ? 'Связь не подтверждена. Повторите чтение измерений.'
          : 'Данные обновились. Повторите чтение измерений.';
      } else if (kind === 'audit' && (state.intent || active)) {
        const intent = state.intent ?? active;
        if (intent) state.intent = { ...intent, retry: changedVersion ? false : intent.retry };
      } else if (!state.intent?.retry) {
        state.intent = {
          after: state.published?.after ?? 0,
          manual: false,
          returnToStart: false,
          retry: false,
        };
      }
      if (changedVersion && kind === 'audit') state.error = '';
    }
    state.id = id;
    state.version = version;
    state.visible = visible;
    state.available = available;
    if (changedId || changedVersion || hidden || disconnected) publish();
    drain();
  }, [id, version, visible, available, kind, read, drain, publish]);

  const beforeClose = useCallback(() => {
    const state = model.current;
    if (!state.open || state.closing) return;
    const active = state.flight?.generation === state.generation ? state.flight.intent : undefined;
    state.closing = true;
    state.generation++;
    state.intent =
      kind === 'audit'
        ? (state.intent ?? (active ? { ...active, retry: false } : null))
        : state.flight
          ? {
              after: state.published?.after ?? 0,
              manual: false,
              returnToStart: false,
              retry: false,
            }
          : null;
    if (kind === 'measurements') state.error = '';
  }, [kind]);
  const setOpen = useCallback(
    (open: boolean) => {
      const state = model.current;
      if (!open && !state.closing) beforeClose();
      state.open = open;
      state.closing = false;
      if (
        open &&
        kind === 'audit' &&
        state.intent?.retry &&
        state.published?.version !== state.version
      )
        state.intent = { ...state.intent, retry: false };
      if (open && (!state.published || state.published.version !== state.version) && !state.intent)
        state.intent = {
          after: state.published?.after ?? 0,
          manual: false,
          returnToStart: false,
          retry: false,
        };
      publish();
      drain();
    },
    [beforeClose, drain, kind, publish],
  );
  const request = useCallback(
    (after = 0, returnToStart = false) => {
      const state = model.current;
      if (state.flight) return;
      state.intent = { after, returnToStart, manual: true, retry: false };
      if (!state.available) {
        state.error =
          kind === 'measurements'
            ? 'Ожидание связи для чтения измерений.'
            : 'Ожидание связи для чтения журнала.';
        if (kind === 'measurements') state.intent.retry = true;
      }
      publish();
      drain();
    },
    [drain, kind, publish],
  );
  const retry = useCallback(() => {
    const intent = model.current.intent;
    if (intent) request(intent.after, intent.returnToStart);
  }, [request]);
  const consumeScroll = useCallback(() => {
    const state = model.current;
    if (
      !state.scrollVersion ||
      state.scrollVersion !== state.generation ||
      !state.available ||
      !state.visible ||
      !state.open ||
      state.closing
    )
      return false;
    state.scrollVersion = 0;
    return true;
  }, []);
  const state = model.current;
  return {
    open: state.open,
    page: state.published?.page ?? null,
    stale: !!state.published && state.published.version !== state.version,
    loading: !!state.flight && state.open && state.visible,
    error: state.error,
    retry: state.intent?.retry ? state.intent : null,
    scrollVersion: state.scrollVersion === state.generation ? state.scrollVersion : 0,
    request,
    retryRead: retry,
    beforeClose,
    setOpen,
    consumeScroll,
  };
}
