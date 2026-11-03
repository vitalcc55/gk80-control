import type {
  ConnectionNotice,
  ConnectionState,
  Event as CoreEvent,
  DesktopStatus,
  SessionView,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

export interface DesktopBridge {
  status(): Promise<DesktopStatus>;
  verify(): Promise<DesktopStatus>;
  subscribe(
    callback: (status: DesktopStatus, polled?: boolean) => void,
    onError?: () => void,
  ): () => void;
}

export interface SessionBridge {
  beginSessionCheck(): Promise<string>;
  keepAlive(ticket: string): Promise<void>;
  sessionView(): Promise<SessionView>;
  observeSession?(): Promise<SessionView>;
  subscribeConnection?(callback: (state: ConnectionState) => void): () => void;
  enableConnectionEvents?(): Promise<void>;
  acknowledgeConnection?(host: string, revision: number): Promise<void>;
  subscribeCoreEvent?(callback: (event: CoreEvent) => void): () => void;
  acknowledgeCoreEvent?(backend: string, sequence: number): Promise<void>;
  waitConnection?(after: number, afterSequence: number): Promise<ConnectionNotice>;
}
