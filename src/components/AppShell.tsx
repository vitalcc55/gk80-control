import { Activity, FileText, ListChecks } from 'lucide-react';
import { type ReactNode, type Ref, useEffect } from 'react';
import type { Screen } from '../model';
import { useFocusRecovery } from './use-focus-recovery';

export const screenTitles: Record<Screen, string> = {
  tests: 'Испытания',
  parameters: 'Параметры',
  readiness: 'Готовность',
  run: 'Проведение',
  result: 'Результат',
};

export function AppShell({
  screen,
  active,
  draftAvailable,
  onTests,
  onRun,
  onParameters,
  onResult,
  mainRef,
  children,
  status,
  notice,
  footer,
  announcement,
  historyResult = false,
}: {
  screen: Screen;
  active: boolean;
  draftAvailable: boolean;
  onTests: () => void;
  onRun: () => void;
  onParameters: () => void;
  onResult?: (() => void) | undefined;
  mainRef?: Ref<HTMLElement>;
  children: ReactNode;
  status?: ReactNode;
  notice?: ReactNode;
  footer?: ReactNode;
  announcement?: ReactNode;
  historyResult?: boolean;
}) {
  const workspaceRef = useFocusRecovery();
  useEffect(() => {
    document.title = `${screenTitles[screen]} — ГК80`;
  }, [screen]);
  const section =
    screen === 'result' && onResult && !historyResult
      ? 'result'
      : screen === 'run' || (screen === 'readiness' && active)
        ? 'run'
        : screen === 'parameters' || screen === 'readiness'
          ? 'parameters'
          : 'tests';
  return (
    <div className={footer ? 'app-shell' : 'app-shell app-shell-live'}>
      <span className="sr-only" role="status" aria-atomic="true">
        {announcement}
      </span>
      <div className="workspace" ref={workspaceRef}>
        <nav className="sidebar" aria-label="Навигация">
          <div className="sidebar-items">
            <div className="brand">
              <div className="brand-symbol">
                <Activity size={20} aria-hidden="true" />
              </div>
              <div>
                <strong>ГК80</strong>
                <span>Испытания клапанов</span>
              </div>
            </div>
            <button
              type="button"
              className={section === 'tests' ? 'nav-item active' : 'nav-item'}
              aria-current={section === 'tests' ? 'page' : undefined}
              onClick={onTests}
            >
              <ListChecks size={18} aria-hidden="true" /> <span>Испытания</span>
            </button>
            {active && (
              <button
                type="button"
                className={section === 'run' ? 'nav-item active' : 'nav-item'}
                aria-current={section === 'run' ? 'page' : undefined}
                onClick={onRun}
              >
                <Activity size={18} aria-hidden="true" />
                <span>Текущее испытание</span>
              </button>
            )}
            {!active && onResult && (
              <button
                type="button"
                className={section === 'result' ? 'nav-item active' : 'nav-item'}
                aria-current={section === 'result' ? 'page' : undefined}
                onClick={onResult}
              >
                <Activity size={18} aria-hidden="true" />
                <span>Результат прохода</span>
              </button>
            )}
            {(active || draftAvailable) && (
              <button
                type="button"
                className={section === 'parameters' ? 'nav-item active' : 'nav-item'}
                aria-current={section === 'parameters' ? 'page' : undefined}
                onClick={onParameters}
              >
                <FileText size={18} aria-hidden="true" />
                <span>Параметры</span>
              </button>
            )}
            {notice && (
              <div className="nav-status" role="status">
                {notice}
              </div>
            )}
          </div>
          {status && (
            // biome-ignore lint/a11y/noNoninteractiveTabindex: keyboard users need to scroll long status messages.
            <section className="sidebar-session" aria-label="Связь и состояние" tabIndex={0}>
              {status}
            </section>
          )}
        </nav>
        <main
          className="main-content"
          ref={mainRef}
          tabIndex={-1}
          aria-label={screenTitles[screen]}
        >
          {children}
        </main>
      </div>
      {footer}
    </div>
  );
}
