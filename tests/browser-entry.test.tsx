// @vitest-environment-options {"url":"http://127.0.0.1:17880/"}
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
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

const generation = '11111111-1111-4111-8111-111111111111:1';
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
  receive(value: object) {
    this.onmessage?.({ data: JSON.stringify(value) });
  }
  receiveRaw(data: string) {
    this.onmessage?.({ data });
  }
}

describe('browser entry', () => {
  beforeEach(() => {
    sessionStorage.clear();
    Socket.instances = [];
    vi.stubGlobal('WebSocket', Socket);
    vi.stubGlobal('fetch', vi.fn());
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('connects automatically and restores the screen after a channel break', async () => {
    render(<BrowserApp />);
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    expect(Socket.instances[0]?.protocols).toEqual(['gk80-v1']);
    expect(fetch).not.toHaveBeenCalled();
    Socket.instances[0]?.receive({ channel: generation });
    expect(await screen.findByText('Рабочий экран')).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Черновик'), { target: { value: 'Клапан' } });
    Socket.instances[0]?.close();
    expect(await screen.findByText(/Подключение…/)).toBeTruthy();
    await waitFor(() => expect(Socket.instances).toHaveLength(2));
    Socket.instances[1]?.receive({ channel: generation });
    expect(await screen.findByText('Рабочий экран')).toBeTruthy();
    expect((screen.getByLabelText('Черновик') as HTMLInputElement).value).toBe('Клапан');
    expect(fetch).not.toHaveBeenCalled();
  });

  it('shows an occupied profile without automatically taking it over', async () => {
    render(<BrowserApp />);
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    Socket.instances[0]?.receive({ busy: true, channel: generation });
    expect((await screen.findByRole('alert')).textContent).toContain('уже открыта');
    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(Socket.instances).toHaveLength(1);
    expect(fetch).not.toHaveBeenCalled();
  });

  it.each([
    `{"channel":"wrong","channel":"${generation}"}`,
    JSON.stringify({ channel: generation, retry: true }),
    JSON.stringify({ busy: true }),
    JSON.stringify({ channel: `${'-'.repeat(36)}:1` }),
  ])('rejects malformed control frames before creating a connected consumer: %s', async (raw) => {
    vi.useFakeTimers();
    render(<BrowserApp />);
    expect(Socket.instances).toHaveLength(1);
    act(() => Socket.instances[0]?.receiveRaw(raw));
    expect(screen.queryByText('Рабочий экран')).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500);
    });
    expect(Socket.instances).toHaveLength(2);
    expect(fetch).not.toHaveBeenCalled();
  });

  it('waits for a previous connection to finish closing', async () => {
    render(<BrowserApp />);
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    Socket.instances[0]?.receive({ retry: true });
    await waitFor(() => expect(Socket.instances).toHaveLength(2));
    Socket.instances[1]?.receive({ channel: generation });
    expect(await screen.findByText('Рабочий экран')).toBeTruthy();
  });

  it('stops retrying if a previous connection does not finish closing', async () => {
    render(<BrowserApp />);
    for (let index = 0; index < 5; index++) {
      await waitFor(() => expect(Socket.instances).toHaveLength(index + 1));
      Socket.instances[index]?.receive({ retry: true });
    }
    expect((await screen.findByRole('alert')).textContent).toContain('уже открыта');
    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(Socket.instances).toHaveLength(5);
  });

  it('retries a briefly busy channel after losing its own connection', async () => {
    render(<BrowserApp />);
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    Socket.instances[0]?.receive({ channel: generation });
    expect(await screen.findByText('Рабочий экран')).toBeTruthy();
    Socket.instances[0]?.close();
    await waitFor(() => expect(Socket.instances).toHaveLength(2));
    Socket.instances[1]?.receive({ busy: true, channel: generation });
    await waitFor(() => expect(Socket.instances).toHaveLength(3));
    Socket.instances[2]?.receive({ channel: generation });
    expect(await screen.findByText('Рабочий экран')).toBeTruthy();
  });

  it('retries a closing previous connection on page refresh', async () => {
    const entry = {
      name: '',
      entryType: 'navigation',
      startTime: 0,
      duration: 0,
      type: 'reload',
      toJSON: () => ({}),
    };
    vi.spyOn(performance, 'getEntriesByType').mockReturnValue([entry]);
    render(<BrowserApp />);
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    Socket.instances[0]?.receive({ busy: true, channel: generation });
    await waitFor(() => expect(Socket.instances).toHaveLength(2));
    Socket.instances[1]?.receive({ channel: generation });
    expect(await screen.findByText('Рабочий экран')).toBeTruthy();
  });

  it('recreates the screen only when the host instance changes', async () => {
    render(<BrowserApp />);
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    Socket.instances[0]?.receive({ channel: generation });
    const input = await screen.findByLabelText('Черновик');
    fireEvent.change(input, { target: { value: 'Клапан' } });
    Socket.instances[0]?.close();
    await waitFor(() => expect(Socket.instances).toHaveLength(2));
    Socket.instances[1]?.receive({ channel: '22222222-2222-4222-8222-222222222222:1' });
    await waitFor(() => expect(screen.getByLabelText('Черновик')).not.toBe(input));
    const replacement = screen.getByLabelText('Черновик');
    expect(replacement).not.toBe(input);
    expect((replacement as HTMLInputElement).value).toBe('');
  });

  it('bounds repeated short-lived channels', async () => {
    render(<BrowserApp />);
    for (let index = 0; index < 5; index++) {
      await waitFor(() => expect(Socket.instances).toHaveLength(index + 1));
      Socket.instances[index]?.receive({ channel: generation });
      Socket.instances[index]?.close();
    }
    expect((await screen.findByRole('alert')).textContent).toContain('не подтверждено');
    expect(Socket.instances).toHaveLength(5);
  });
});
