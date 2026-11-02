import { type FormEvent, useEffect, useRef, useState } from 'react';
import { createBrowserBridge } from '../desktop/browser-bridge';
import { DesktopApp } from '../desktop/DesktopApp';

const proofKey = 'gk80.browser.session';
const deniedKey = 'gk80.browser.denied';
const token = /^[A-Za-z0-9_-]{32}$/;
type Bridges = ReturnType<typeof createBrowserBridge>;
type Screen =
  | 'locking'
  | 'pair'
  | 'pairing'
  | 'connecting'
  | 'connected'
  | 'unavailable'
  | 'blocked';

export function BrowserApp() {
  const [screen, setScreen] = useState<Screen>('locking');
  const [code, setCode] = useState('');
  const [bridges, setBridges] = useState<Bridges | null>(null);
  const proof = useRef('');
  const pendingStorage = useRef<Storage | null>(null);
  const held = useRef(false);
  const pairing = useRef(false);
  const pairingRequest = useRef<AbortController | null>(null);
  const shouldConnect = screen === 'connecting' || screen === 'connected';

  useEffect(() => {
    let disposed = false;
    let release = () => {};
    const deny = () => {
      try {
        sessionStorage.removeItem(proofKey);
        sessionStorage.setItem(deniedKey, '1');
      } catch {
        // Storage is unavailable; this page still refuses access.
      }
      setScreen('blocked');
    };
    if (window.location.origin !== 'http://127.0.0.1:17880') {
      setScreen('blocked');
      return;
    }
    if (!navigator.locks) {
      deny();
      return;
    }
    try {
      if (sessionStorage.getItem(deniedKey) === '1') {
        setScreen('blocked');
        return;
      }
    } catch {
      setScreen('blocked');
      return;
    }
    void navigator.locks
      .request('gk80-demo-client', { mode: 'exclusive', ifAvailable: true }, async (lock) => {
        if (disposed) return;
        if (!lock) {
          deny();
          return;
        }
        held.current = true;
        try {
          pendingStorage.current = window.localStorage;
          pendingStorage.current.getItem('gk80.browser.storage-check');
          const saved = sessionStorage.getItem(proofKey);
          if (saved && token.test(saved)) {
            proof.current = saved;
            setScreen('connecting');
          } else {
            setScreen('pair');
          }
        } catch {
          setScreen('blocked');
        }
        await new Promise<void>((resolve) => {
          release = resolve;
        });
        held.current = false;
      })
      .catch(() => {
        if (!disposed) deny();
      });
    return () => {
      disposed = true;
      held.current = false;
      pairingRequest.current?.abort();
      release();
    };
  }, []);

  useEffect(() => {
    if (!shouldConnect || !proof.current || !held.current) return;
    let disposed = false;
    let socket: WebSocket | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let timeout: ReturnType<typeof setTimeout> | undefined;
    let stable: ReturnType<typeof setTimeout> | undefined;
    let channel: AbortController | undefined;
    let failures = 0;
    const connect = () => {
      if (disposed) return;
      let accepted = false;
      try {
        socket = new WebSocket('ws://127.0.0.1:17880/api/session/stream', [
          'gk80-v1',
          `gk80-proof.${proof.current}`,
        ]);
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
          const data: unknown = JSON.parse(event.data);
          if (
            typeof data !== 'object' ||
            data === null ||
            Array.isArray(data) ||
            Object.keys(data).length !== 1 ||
            !('channel' in data) ||
            typeof data.channel !== 'string' ||
            !token.test(data.channel)
          )
            throw new Error('invalid channel');
          accepted = true;
          if (timeout) clearTimeout(timeout);
          stable = setTimeout(() => {
            failures = 0;
          }, 10000);
          channel = new AbortController();
          setBridges(
            createBrowserBridge(proof.current, {
              capability: data.channel,
              signal: channel.signal,
            }),
          );
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
        if (disposed) return;
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

  async function pair(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!held.current || pairing.current || !token.test(code)) return;
    const submitted = code;
    setCode('');
    pairing.current = true;
    setScreen('pairing');
    const controller = new AbortController();
    pairingRequest.current = controller;
    const timeout = setTimeout(() => controller.abort(), 3500);
    try {
      const response = await fetch('/api/session/pair', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
        body: JSON.stringify({ code: submitted }),
        signal: controller.signal,
      });
      if (!response.ok) throw new Error('pair unavailable');
      const data: unknown = await response.json();
      if (
        typeof data !== 'object' ||
        data === null ||
        Array.isArray(data) ||
        Object.keys(data).length !== 1 ||
        !('proof' in data) ||
        typeof data.proof !== 'string' ||
        !token.test(data.proof) ||
        !held.current
      )
        throw new Error('invalid response');
      sessionStorage.setItem(proofKey, data.proof);
      proof.current = data.proof;
      setScreen('connecting');
    } catch {
      if (held.current) setScreen('unavailable');
    } finally {
      clearTimeout(timeout);
      if (pairingRequest.current === controller) pairingRequest.current = null;
      pairing.current = false;
    }
  }

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
            {screen === 'pair' && (
              <form onSubmit={pair}>
                <label htmlFor="pair-code">Код из окна запуска</label>
                <input
                  id="pair-code"
                  type="password"
                  value={code}
                  onChange={(event) => setCode(event.target.value)}
                  autoComplete="off"
                />
                <button type="submit" disabled={!token.test(code)}>
                  Подключить
                </button>
              </form>
            )}
            {(screen === 'locking' || screen === 'pairing' || screen === 'connecting') && (
              <p role="status">Подключение… Показания не подтверждены.</p>
            )}
            {screen === 'blocked' && (
              <div role="alert">
                <p>
                  Не удалось получить доступ к демонстрации. Проверьте адрес и закройте другую
                  вкладку ГК80. Для нового сопряжения перезапустите демонстрацию и получите новый
                  код.
                </p>
                <button
                  type="button"
                  onClick={() => {
                    try {
                      sessionStorage.removeItem(deniedKey);
                      window.location.reload();
                    } catch {
                      setScreen('blocked');
                    }
                  }}
                >
                  Начать новое подключение
                </button>
              </div>
            )}
            {screen === 'unavailable' && (
              <div role="alert">
                <p>
                  Подключение не подтверждено. Если демонстрация уже перезапущена, выберите «Ввести
                  новый код» и используйте код текущего окна. Если ответ на отправку кода потерян,
                  перезапустите демонстрацию.
                </p>
                {proof.current && (
                  <button
                    type="button"
                    onClick={() => {
                      setScreen('connecting');
                    }}
                  >
                    Повторить подключение
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => {
                    try {
                      sessionStorage.removeItem(proofKey);
                      proof.current = '';
                      setBridges(null);
                      setScreen('pair');
                    } catch {
                      setScreen('blocked');
                    }
                  }}
                >
                  Ввести новый код
                </button>
              </div>
            )}
          </section>
        </main>
      )}
      {bridges && pendingStorage.current && (
        <div style={{ display: screen === 'connected' ? undefined : 'none' }}>
          <DesktopApp {...bridges} pendingStorage={pendingStorage.current} retainLiveOnError />
        </div>
      )}
    </>
  );
}
