import { useLayoutEffect, useRef } from 'react';
import type { Screen } from '../model';

/** Focus follows an operator's requested destination, never a telemetry update. */
export function useScreenFocus(screen: Screen) {
  const mainRef = useRef<HTMLElement>(null);
  const requested = useRef<Screen | null>(screen);
  const requestFocus = (destination: Screen) => {
    if (destination === screen) {
      mainRef.current?.focus();
      requested.current = null;
    } else requested.current = destination;
  };
  useLayoutEffect(() => {
    if (requested.current === screen) {
      requested.current = null;
      mainRef.current?.focus();
    }
  }, [screen]);
  return { mainRef, requestFocus };
}
