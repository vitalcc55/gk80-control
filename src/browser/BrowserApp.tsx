import { useEffect, useRef, useState } from 'react';
import { createBrowserBridge } from '../desktop/browser-bridge';
import { parseBrowserJSON, responseGuards } from '../desktop/browser-responses';
import { DesktopApp } from '../desktop/DesktopApp';
import { MeasurementSeriesContext } from '../desktop/measurement-series';

type Bridges = ReturnType<typeof createBrowserBridge>;
type Connection = { bridges: Bridges; instance: string };
type Screen = 'connecting' | 'connected' | 'busy' | 'unavailable';

export function BrowserApp() {
  const [measurementContext] = useState(() => new MeasurementSeriesContext());
  const [screen, setScreen] = useState<Screen>('connecting');
  const [connection, setConnection] = useState<Connection | null>(null);
  const pendingStorage = useRef<Storage | null>(null);
  const shouldConnect = screen === 'connecting' || screen === 'connected';

  useEffect(() => {
    if (!shouldConnect) return;
    if (window.location.origin !== 'http://127.0.0.1:17880') {
      setScreen('unavailable');
      return;
    }
    try {
      pendingStorage.current = window.localStorage;
      pendingStorage.current.getItem('gk80.browser.storage-check');
    } catch {
      setScreen('unavailable');
      return;
    }
    let disposed = false;
    let socket: WebSocket | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let timeout: ReturnType<typeof setTimeout> | undefined;
    let stable: ReturnType<typeof setTimeout> | undefined;
    let channel: AbortController | undefined;
    let failures = 0;
    let recovering = performance
      .getEntriesByType('navigation')
      .some((entry) => 'type' in entry && entry.type === 'reload');
    const connect = () => {
      if (disposed) return;
      let accepted = false;
      let busy = false;
      try {
        socket = new WebSocket('ws://127.0.0.1:17880/api/session/stream', ['gk80-v1']);
      } catch {
        setScreen('unavailable');
        return;
      }
      const current = socket;
      timeout = setTimeout(() => current.close(), 3500);
      current.onmessage = (event) => {
        if (
          disposed ||
          accepted ||
          current.protocol !== 'gk80-v1' ||
          typeof event.data !== 'string'
        ) {
          current.close();
          return;
        }
        try {
          const data = parseBrowserJSON(event.data);
          if (!responseGuards.BrowserControl(data)) throw new Error('invalid channel');
          if (data['busy'] === true || data['retry'] === true) {
            const closing = data['retry'] === true;
            if ((!closing && !recovering) || failures >= 4) {
              busy = true;
              setScreen('busy');
            }
            current.close();
            return;
          }
          const generation = data['channel'];
          if (typeof generation !== 'string') throw new Error('invalid channel');
          accepted = true;
          recovering = false;
          if (timeout) clearTimeout(timeout);
          stable = setTimeout(() => {
            failures = 0;
          }, 10000);
          channel = new AbortController();
          setConnection({
            bridges: createBrowserBridge({ generation, signal: channel.signal }),
            instance: generation.split(':')[0] ?? '',
          });
          setScreen('connected');
        } catch {
          current.close();
        }
      };
      current.onclose = () => {
        if (timeout) clearTimeout(timeout);
        if (stable) clearTimeout(stable);
        channel?.abort();
        channel = undefined;
        if (disposed || busy) return;
        if (accepted) recovering = true;
        setScreen('connecting');
        failures += 1;
        if (failures >= 5) setScreen('unavailable');
        else timer = setTimeout(connect, 500);
      };
      current.onerror = () => current.close();
    };
    connect();
    return () => {
      disposed = true;
      if (timer) clearTimeout(timer);
      if (timeout) clearTimeout(timeout);
      if (stable) clearTimeout(stable);
      channel?.abort();
      socket?.close();
    };
  }, [shouldConnect]);

  return (
    <>
      {screen !== 'connected' && (
        <main className="desktop-shell">
          <header className="desktop-header">
            <strong>ГК80</strong>
            <span>Демонстрация · тестовые данные</span>
          </header>
          <section className="desktop-card">
            <h1>Подключение к демонстрации</h1>
            {screen === 'connecting' && (
              <p role="status">Подключение… Показания не подтверждены.</p>
            )}
            {screen === 'busy' && <p role="alert">Демонстрация уже открыта в другом интерфейсе.</p>}
            {screen === 'unavailable' && (
              <p role="alert">Подключение не подтверждено. Проверьте, что демонстрация запущена.</p>
            )}
            {(screen === 'busy' || screen === 'unavailable') && (
              <button type="button" onClick={() => setScreen('connecting')}>
                Повторить подключение
              </button>
            )}
          </section>
        </main>
      )}
      {connection && pendingStorage.current && (
        <div style={{ display: screen === 'connected' ? undefined : 'none' }}>
          <DesktopApp
            measurementContext={measurementContext}
            key={connection.instance}
            {...connection.bridges}
            pendingStorage={pendingStorage.current}
            retainLiveOnError
          />
        </div>
      )}
    </>
  );
}
