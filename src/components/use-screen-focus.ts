import { useEffect, useLayoutEffect, useRef } from 'react';
import type { Screen } from '../model';

/** Focus follows an operator's requested destination, never a telemetry update. */
export function useScreenFocus(screen: Screen) {
  const mainRef = useRef<HTMLElement>(null);
  const requested = useRef<Screen | null>(screen);
  const initial = useRef(true);
  useEffect(() => {
    const cancel = () => {
      requested.current = null;
    };
    const onFocus = (event: FocusEvent) => {
      if (event.target !== mainRef.current) cancel();
    };
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') cancel();
    };
    document.addEventListener('pointerdown', cancel, true);
    document.addEventListener('keydown', cancel, true);
    document.addEventListener('focusin', onFocus, true);
    document.addEventListener('visibilitychange', onVisibility);
    window.addEventListener('blur', cancel);
    return () => {
      cancel();
      document.removeEventListener('pointerdown', cancel, true);
      document.removeEventListener('keydown', cancel, true);
      document.removeEventListener('focusin', onFocus, true);
      document.removeEventListener('visibilitychange', onVisibility);
      window.removeEventListener('blur', cancel);
    };
  }, []);
  const requestFocus = (destination: Screen) => {
    if (destination === screen) {
      if (document.hasFocus()) mainRef.current?.focus();
      requested.current = null;
    } else requested.current = destination;
  };
  useLayoutEffect(() => {
    if (requested.current === screen) {
      requested.current = null;
      const wasInitial = initial.current;
      initial.current = false;
      if (
        document.hasFocus() &&
        (!wasInitial ||
          document.activeElement === document.body ||
          document.activeElement === mainRef.current)
      )
        mainRef.current?.focus();
    }
  }, [screen]);
  return { mainRef, requestFocus };
}
