import { Activity, FileText, ListChecks } from 'lucide-react';
import { type ReactNode, type Ref, useEffect } from 'react';
import type { Screen } from '../model';

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
  mainRef,
  children,
  status,
  notice,
  footer,
  announcement,
}: {
  screen: Screen;
  active: boolean;
  draftAvailable: boolean;
  onTests: () => void;
  onRun: () => void;
  onParameters: () => void;
  mainRef?: Ref<HTMLElement>;
  children: ReactNode;
  status?: ReactNode;
  notice?: ReactNode;
  footer?: ReactNode;
  announcement?: ReactNode;
}) {
  useEffect(() => {
    document.title = `${screenTitles[screen]} — ГК80`;
  }, [screen]);
  const section =
    screen === 'run' || (screen === 'readiness' && active)
      ? 'run'
      : screen === 'parameters' || screen === 'readiness'
        ? 'parameters'
        : 'tests';
  return (
    <div className={footer ? 'app-shell' : 'app-shell app-shell-live'}>
      {announcement && (
        <span className="sr-only" role="status" aria-atomic="true">
          {announcement}
        </span>
      )}
      <div className="workspace">
        <nav className="sidebar" aria-label="Навигация">
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
            <ListChecks size={18} aria-hidden="true" /> Испытания
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
        </nav>
        <main
          className="main-content"
          ref={mainRef}
          tabIndex={-1}
          aria-label={screenTitles[screen]}
        >
          {status}
          {children}
        </main>
      </div>
      {footer}
    </div>
  );
}
