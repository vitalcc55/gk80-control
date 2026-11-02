import type {
  AuditPage,
  CommandDispatch,
  CommandStatus,
  ExactLookupResult,
  ListTestsParams,
  MeasurementPage,
  Readiness,
  TestList,
  TestRecord,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type {
  ConfirmCommandInput,
  CreateCommandInput,
  StartCommandInput,
  StopCommandInput,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/desktop/models';

// A confirmed conflict rejects this attempt; the old command is a different action.
export class CommandConflictError extends Error {
  constructor() {
    super('command identifier belongs to another command');
    this.name = 'CommandConflictError';
  }
}

// Replacements within one host/backend session must address the same demo profile and core.
// The caller must preserve that profile binding when reconciling unresolved commands.
export interface DemoService {
  createTest(command: CreateCommandInput): Promise<CommandDispatch>;
  readiness(testId: string): Promise<Readiness>;
  startTest(testId: string, command: StartCommandInput): Promise<CommandDispatch>;
  confirmAction(
    testId: string,
    actionId: string,
    command: ConfirmCommandInput,
  ): Promise<CommandDispatch>;
  stopTest(testId: string, command: StopCommandInput): Promise<CommandStatus>;
  command(commandId: string): Promise<CommandStatus>;
  lookupCommandExact(
    databaseId: string,
    kind: 'create' | 'start' | 'confirm' | 'stop',
    testId: string,
    actionId: string,
    commandId: string,
    originalJSON: string,
  ): Promise<ExactLookupResult>;
  listTests(params: ListTestsParams): Promise<TestList>;
  test(testId: string): Promise<TestRecord>;
  measurements(testId: string, after: number, limit: number): Promise<MeasurementPage>;
  audit(testId: string, after: number, limit: number): Promise<AuditPage>;
}
