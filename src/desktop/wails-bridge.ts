import { Events } from '@wailsio/runtime';
import '../generated/wails/events/index';
import {
  Status,
  Verify,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/bootstrapservice';
import {
  AcknowledgeConnection,
  AcknowledgeCoreEvent,
  Audit,
  BeginSessionCheck,
  Command,
  ConfirmAction,
  CreateTest,
  EnableConnectionEvents,
  KeepAlive,
  ListTests,
  Measurements,
  ObserveSession,
  Readiness,
  SessionView,
  StartTest,
  StopTest,
  Test,
  WaitConnection,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/service';
import type { DesktopBridge, SessionBridge } from './bridge';
import type { DemoService } from './demo-service';

export const sessionBridge: SessionBridge = {
  beginSessionCheck: BeginSessionCheck,
  keepAlive: KeepAlive,
  sessionView: SessionView,
  observeSession: ObserveSession,
  subscribeConnection: (callback) => Events.On('core:connection', (event) => callback(event.data)),
  enableConnectionEvents: EnableConnectionEvents,
  acknowledgeConnection: AcknowledgeConnection,
  subscribeCoreEvent: (callback) => Events.On('core:event', (event) => callback(event.data)),
  acknowledgeCoreEvent: AcknowledgeCoreEvent,
  waitConnection: WaitConnection,
};

export const desktopBridge: DesktopBridge = {
  status: Status,
  verify: Verify,
  subscribe: (callback) => Events.On('desktop:status', (event) => callback(event.data)),
};

export const desktopDemoService: DemoService = {
  createTest: CreateTest,
  readiness: Readiness,
  startTest: StartTest,
  confirmAction: ConfirmAction,
  stopTest: StopTest,
  command: Command,
  listTests: ListTests,
  test: Test,
  measurements: Measurements,
  audit: Audit,
};
