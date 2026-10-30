import { Events } from '@wailsio/runtime';
import '../generated/wails/events/index';
import type {
  DesktopStatus,
  SessionView,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import {
  Status,
  Verify,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/bootstrapservice';
import {
  BeginSessionCheck,
  KeepAlive,
  SessionView as ReadSessionView,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/service';

export interface DesktopBridge {
  status(): Promise<DesktopStatus>;
  verify(): Promise<DesktopStatus>;
  subscribe(callback: (status: DesktopStatus) => void): () => void;
}

export interface SessionBridge {
  beginSessionCheck(): Promise<string>;
  keepAlive(ticket: string): Promise<void>;
  sessionView(): Promise<SessionView>;
}

export const sessionBridge: SessionBridge = {
  beginSessionCheck: BeginSessionCheck,
  keepAlive: KeepAlive,
  sessionView: ReadSessionView,
};

type SessionResult = { view: SessionView; started: number } | null;
type SessionListener = (result: SessionResult) => void;
const pendingSessions = new WeakMap<SessionBridge, Set<SessionListener>>();

// Share one in-flight check across a React remount; a late Wails request must not queue indefinitely.
export function readSession(bridge: SessionBridge, listener: SessionListener): () => void {
  let listeners = pendingSessions.get(bridge);
  if (!listeners) {
    listeners = new Set();
    pendingSessions.set(bridge, listeners);
    const current = listeners;
    const check = async (): Promise<SessionResult> => {
      const ticket = await bridge.beginSessionCheck();
      if (current.size === 0) return null;
      await bridge.keepAlive(ticket);
      if (current.size === 0) return null;
      const started = performance.now();
      return { view: await bridge.sessionView(), started };
    };
    void check().then(
      (result) => {
        pendingSessions.delete(bridge);
        for (const callback of current) callback(result);
      },
      () => {
        pendingSessions.delete(bridge);
        for (const callback of current) callback(null);
      },
    );
  }
  listeners.add(listener);
  const current = listeners;
  return () => {
    current.delete(listener);
  };
}

export const desktopBridge: DesktopBridge = {
  status: Status,
  verify: Verify,
  subscribe: (callback) => Events.On('desktop:status', (event) => callback(event.data)),
};
