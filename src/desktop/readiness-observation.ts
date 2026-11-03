import type { Readiness } from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { DemoService } from './demo-service';

type ReadinessRead = {
  listeners: Set<(value: Readiness | null) => void>;
  inFlight: number;
  attempt: number;
  completed: number;
  success: boolean;
  timer: ReturnType<typeof setTimeout> | undefined;
  start: () => void;
};
const readinessReadsByService = new Map<DemoService, Map<string, ReadinessRead>>();
let readinessReadCount = 0;

export function observeReadiness(
  service: DemoService,
  hostRunId: string,
  backendRunId: string,
  testId: string,
  revision: number,
  listener: (value: Readiness | null) => void,
) {
  let readinessReads = readinessReadsByService.get(service);
  if (!readinessReads) {
    readinessReads = new Map<string, ReadinessRead>();
    readinessReadsByService.set(service, readinessReads);
  }
  const reads = readinessReads;
  const key = `${hostRunId}:${backendRunId}:${testId}:${revision}`;
  let pending = reads.get(key);
  if (!pending) {
    if (readinessReadCount >= 64) {
      if (!reads.size) readinessReadsByService.delete(service);
      queueMicrotask(() => listener(null));
      return () => {};
    }
    pending = {
      listeners: new Set(),
      inFlight: 0,
      attempt: 0,
      completed: 0,
      success: false,
      timer: undefined,
      start: () => {},
    };
    reads.set(key, pending);
    readinessReadCount += 1;
    const current = pending;
    const forgetRead = () => {
      if (reads.get(key) !== current) return;
      reads.delete(key);
      readinessReadCount -= 1;
      if (!reads.size) readinessReadsByService.delete(service);
    };
    const schedule = (delay: number) => {
      if (!current.listeners.size || current.success || current.timer) return;
      current.timer = setTimeout(() => {
        current.timer = undefined;
        current.start();
      }, delay);
    };
    current.start = () => {
      if (!current.listeners.size || current.success || current.inFlight >= 2) return;
      current.inFlight += 1;
      const attempt = ++current.attempt;
      const watchdog = setTimeout(() => schedule(0), 4000);
      void service
        .readiness(testId)
        .then(
          (value) => {
            if (attempt <= current.completed) return;
            current.completed = attempt;
            const result = value.testId === testId && value.revision === revision ? value : null;
            current.success = result !== null && current.listeners.size > 0;
            for (const callback of current.listeners) callback(result);
          },
          () => {
            if (attempt <= current.completed) return;
            current.completed = attempt;
            for (const callback of current.listeners) callback(null);
          },
        )
        .finally(() => {
          clearTimeout(watchdog);
          current.inFlight -= 1;
          if (current.listeners.size) schedule(2000);
          else if (!current.inFlight) forgetRead();
        });
    };
  }
  const current = pending;
  current.listeners.add(listener);
  if (!current.inFlight) current.start();
  else if (!current.timer && !current.success)
    current.timer = setTimeout(() => {
      current.timer = undefined;
      current.start();
    }, 4000);
  return () => {
    current.listeners.delete(listener);
    if (!current.listeners.size) {
      current.success = false;
      clearTimeout(current.timer);
      current.timer = undefined;
      if (!current.inFlight && reads.get(key) === current) {
        reads.delete(key);
        readinessReadCount -= 1;
        if (!reads.size) readinessReadsByService.delete(service);
      }
    }
  };
}
