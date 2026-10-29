import { Events } from '@wailsio/runtime';
import '../generated/wails/events/index';
import type { DesktopStatus } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import {
  Status,
  Verify,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/bootstrapservice';

export interface DesktopBridge {
  status(): Promise<DesktopStatus>;
  verify(): Promise<DesktopStatus>;
  subscribe(callback: (status: DesktopStatus) => void): () => void;
}

export const desktopBridge: DesktopBridge = {
  status: Status,
  verify: Verify,
  subscribe: (callback) => Events.On('desktop:status', (event) => callback(event.data)),
};
