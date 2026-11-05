import { useEffect, useRef } from 'react';

/** Keep the keyboard in the current screen when an update removes its focused control. */
export function useFocusRecovery() {
  const workspaceRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const currentWorkspace = workspaceRef.current;
    if (!currentWorkspace) return;
    const workspace: HTMLDivElement = currentWorkspace;
    let focused: HTMLElement | null = null;
    const observer = new MutationObserver(() => {
      if (!focused) return;
      const unavailable =
        !focused.isConnected ||
        !!focused.closest('[hidden]') ||
        (focused instanceof HTMLButtonElement && focused.disabled);
      if (!unavailable || !document.hasFocus()) return;
      if (document.activeElement !== document.body && document.activeElement !== focused) return;
      const main = workspace.querySelector('main');
      if (!main?.isConnected || main.closest('[hidden]')) return;
      focused = null;
      observer.disconnect();
      main.focus({ preventScroll: true });
    });
    function clear() {
      focused = null;
      observer.disconnect();
    }
    function track() {
      clear();
      const active = document.activeElement;
      if (!(active instanceof HTMLElement) || !workspace.contains(active)) return;
      focused = active;
      for (let element: HTMLElement | null = active; element; element = element.parentElement) {
        observer.observe(element, {
          childList: true,
          attributes: true,
          attributeFilter: ['hidden', 'disabled'],
        });
        if (element === workspace) break;
      }
    }
    function onPointerDown(event: PointerEvent) {
      if (!(event.target instanceof Node) || !focused?.contains(event.target)) clear();
    }
    function onVisibility() {
      if (document.visibilityState === 'hidden') clear();
      else track();
    }
    document.addEventListener('focusin', track);
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('visibilitychange', onVisibility);
    window.addEventListener('blur', clear);
    window.addEventListener('focus', track);
    track();
    return () => {
      clear();
      document.removeEventListener('focusin', track);
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('visibilitychange', onVisibility);
      window.removeEventListener('blur', clear);
      window.removeEventListener('focus', track);
    };
  }, []);
  return workspaceRef;
}
