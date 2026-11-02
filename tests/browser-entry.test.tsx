// @vitest-environment-options {"url":"http://127.0.0.1:17880/"}
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BrowserApp } from '../src/browser/BrowserApp';

vi.mock('../src/desktop/DesktopApp', () => ({
  DesktopApp: () => (
    <>
      <p>Рабочий экран</p>
      <input aria-label="Черновик" />
    </>
  ),
}));

const proof = 'p'.repeat(32);
const channel = 'c'.repeat(32);
const code = 'a'.repeat(32);

class Socket {
  static instances: Socket[] = [];
  protocol = 'gk80-v1';
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(
    public url: string,
    public protocols: string[],
  ) {
    Socket.instances.push(this);
  }
  close() {
    this.onclose?.();
  }
  accept() {
    this.onmessage?.({ data: JSON.stringify({ channel }) });
  }
}

describe('browser entry', () => {
  beforeEach(() => {
    sessionStorage.clear();
    Socket.instances = [];
    vi.stubGlobal('WebSocket', Socket);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ proof }) }));
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: {
        request: (_name: string, _options: object, callback: (lock: object) => Promise<void>) =>
          callback({}),
      },
    });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('pairs once and reconnects the same session without another POST', async () => {
    render(<BrowserApp />);
    fireEvent.change(await screen.findByLabelText('Код из окна запуска'), {
      target: { value: code },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Подключить' }));
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith(
      '/api/session/pair',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ code }),
        redirect: 'error',
      }),
    );
    expect(Socket.instances[0]?.protocols).toEqual(['gk80-v1', `gk80-proof.${proof}`]);
    Socket.instances[0]?.accept();
    expect(await screen.findByText('Рабочий экран')).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Черновик'), { target: { value: 'Клапан' } });
    Socket.instances[0]?.close();
    expect(await screen.findByText(/Подключение…/)).toBeTruthy();
    await waitFor(() => expect(Socket.instances).toHaveLength(2));
    Socket.instances[1]?.accept();
    expect(await screen.findByText('Рабочий экран')).toBeTruthy();
    expect((screen.getByLabelText('Черновик') as HTMLInputElement).value).toBe('Клапан');
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(sessionStorage.getItem('gk80.browser.session')).toBe(proof);
  });

  it('restores the session after refresh without pairing again', async () => {
    sessionStorage.setItem('gk80.browser.session', proof);
    render(<BrowserApp />);
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    Socket.instances[0]?.accept();
    expect(await screen.findByText('Рабочий экран')).toBeTruthy();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('does not connect another tab when the lock is occupied', async () => {
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: {
        request: (_name: string, _options: object, callback: (lock: null) => Promise<void>) =>
          callback(null),
      },
    });
    sessionStorage.setItem('gk80.browser.session', proof);
    render(<BrowserApp />);
    expect((await screen.findByRole('alert')).textContent).toContain('закройте другую вкладку');
    expect(Socket.instances).toHaveLength(0);
    expect(fetch).not.toHaveBeenCalled();
    expect(sessionStorage.getItem('gk80.browser.session')).toBeNull();
    expect(sessionStorage.getItem('gk80.browser.denied')).toBe('1');
  });

  it('does not repeat a pairing request whose response is unknown', async () => {
    vi.mocked(fetch).mockRejectedValue(new Error('connection lost'));
    render(<BrowserApp />);
    fireEvent.change(await screen.findByLabelText('Код из окна запуска'), {
      target: { value: code },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Подключить' }));
    expect((await screen.findByRole('alert')).textContent).toContain('перезапустите демонстрацию');
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(Socket.instances).toHaveLength(0);
  });

  it('aborts pairing before releasing the tab on unmount', async () => {
    vi.mocked(fetch).mockImplementation(() => new Promise(() => {}));
    const view = render(<BrowserApp />);
    fireEvent.change(await screen.findByLabelText('Код из окна запуска'), {
      target: { value: code },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Подключить' }));
    const request = vi.mocked(fetch).mock.calls[0]?.[1];
    expect(request?.signal?.aborted).toBe(false);
    view.unmount();
    expect(request?.signal?.aborted).toBe(true);
  });

  it('stops repeated short-lived channels instead of reconnecting indefinitely', async () => {
    sessionStorage.setItem('gk80.browser.session', proof);
    render(<BrowserApp />);
    for (let index = 0; index < 5; index++) {
      await waitFor(() => expect(Socket.instances).toHaveLength(index + 1));
      Socket.instances[index]?.accept();
      Socket.instances[index]?.close();
    }
    expect((await screen.findByRole('alert')).textContent).toContain('не подтверждено');
    expect(Socket.instances).toHaveLength(5);
  });
});
