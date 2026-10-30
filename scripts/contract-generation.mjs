import { spawnSync } from 'node:child_process';
import { copyFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const write = process.argv.slice(2).includes('--write');
const extraArguments = process.argv.slice(2).filter((argument) => argument !== '--write');
if (extraArguments.length > 0) throw new Error(`Unknown arguments: ${extraArguments.join(', ')}`);

const scratchRoot = join(root, '.tmp', '.codex');
mkdirSync(scratchRoot, { recursive: true });
const scratch = mkdtempSync(join(scratchRoot, 'contract-generation-'));
cpSync(join(root, 'api'), join(scratch, 'api'), { recursive: true });
cpSync(join(root, 'internal'), join(scratch, 'internal'), { recursive: true });
copyFileSync(join(root, 'go.mod'), join(scratch, 'go.mod'));
copyFileSync(join(root, 'go.sum'), join(scratch, 'go.sum'));
const goModel = join(scratch, 'internal', 'contract', 'models.gen.go');
const wailsOutput = join(scratch, 'wails');

function run(command, args) {
  const result = spawnSync(command, args, { cwd: scratch, encoding: 'utf8' });
  if (result.error || result.status !== 0) {
    throw new Error(`${command} failed: ${result.error ?? result.stderr ?? result.stdout}`);
  }
  return `${result.stdout}${result.stderr}`;
}

const go = process.platform === 'win32' ? 'go.exe' : 'go';
const wails = process.platform === 'win32' ? 'wails3.exe' : 'wails3';
if (run(wails, ['version']).trim() !== 'v3.0.0') throw new Error('Wails CLI 3.0.0 is required');
run(go, ['tool', 'oapi-codegen', '-config', 'api/oapi-codegen.yaml', '-o', goModel, 'api/core.yaml']);
const generationLog = run(wails, ['generate', 'bindings', '-ts', '-i', '-time-type', 'string', '-d', wailsOutput, './internal/desktop']);
if (!/2 Services, 17 Methods,/.test(generationLog) || !/3 Events/.test(generationLog) || /\bWARN(?:ING)?\b/i.test(generationLog)) {
  throw new Error(`Unexpected Wails generation result: ${generationLog}`);
}

function filesIn(folder) {
  if (!existsSync(folder)) return [];
  return readdirSync(folder, { withFileTypes: true }).flatMap((entry) => {
    const path = join(folder, entry.name);
    return entry.isDirectory() ? filesIn(path) : [path];
  });
}

const expectedService = join(wailsOutput, 'github.com', 'vitalcc55', 'gk80-control', 'internal', 'desktop', 'service.ts');
const expectedEvent = join(wailsOutput, 'github.com', 'wailsapp', 'wails', 'v3', 'internal', 'eventdata.d.ts');
const expectedModels = join(wailsOutput, 'github.com', 'vitalcc55', 'gk80-control', 'internal', 'contract', 'models.ts');
const expectedInputs = join(wailsOutput, 'github.com', 'vitalcc55', 'gk80-control', 'internal', 'desktop', 'models.ts');
for (const path of [expectedService, expectedEvent, expectedModels, expectedInputs]) {
  if (!existsSync(path)) throw new Error(`Missing generated file: ${path}`);
}
const inputTypes = readFileSync(expectedInputs, 'utf8');
for (const [name, fields] of [
  ['CreateCommandInput', ['commandId', 'expectedRevision', 'parameters']],
  ['StartCommandInput', ['commandId', 'connectionConfirmed', 'expectedRevision', 'valveClosed']],
  ['ConfirmCommandInput', ['commandId', 'expectedRevision']],
  ['StopCommandInput', ['commandId']],
]) {
  const match = inputTypes.match(new RegExp(`export interface ${name} \\{([\\s\\S]*?)\\}`));
  if (!match) throw new Error(`Missing generated command input: ${name}`);
  const generated = [...match[1].matchAll(/^\s*"([^"]+)"(\?)?:/gm)];
  if (generated.some((item) => item[2]) || generated.map((item) => item[1]).sort().join(',') !== [...fields].sort().join(',')) {
    throw new Error(`Unexpected generated command input shape: ${name}`);
  }
}
const eventTypes = readFileSync(expectedEvent, 'utf8');
if (!eventTypes.includes('"core:event": contract$0.Event') ||
    !eventTypes.includes('"core:connection": contract$0.ConnectionState') ||
    !eventTypes.includes('"desktop:status": contract$0.DesktopStatus')) {
  throw new Error('Generated event types are missing');
}
const require = createRequire(import.meta.url);
const biome = require.resolve('@biomejs/biome/bin/biome');
writeFileSync(join(scratch, 'biome.json'), JSON.stringify({ linter: { rules: { suspicious: { noExplicitAny: 'error' } } } }));
run(process.execPath, [biome, 'lint', '--only=suspicious/noExplicitAny', '--error-on-warnings', '--config-path', scratch, wailsOutput]);
for (const path of filesIn(wailsOutput)) {
  if (path.endsWith('.ts')) {
    writeFileSync(path, readFileSync(path, 'utf8').replace(/\n+$/, '\n'));
  }
}

const generatedRoot = join(root, 'src', 'generated', 'wails');
const pairs = [
  [goModel, join(root, 'internal', 'contract', 'models.gen.go')],
  ...filesIn(wailsOutput).map((path) => [path, join(generatedRoot, relative(wailsOutput, path))]),
];
const expectedPaths = new Set(pairs.map(([, target]) => target));
const extra = filesIn(generatedRoot).filter((path) => !expectedPaths.has(path));
if (extra.length > 0) throw new Error(`Unexpected generated files require manual inspection: ${extra.join(', ')}`);
const changed = pairs.filter(([source, target]) => !existsSync(target) || !readFileSync(source).equals(readFileSync(target)));
if (write) {
  for (const [source, target] of changed) {
    mkdirSync(dirname(target), { recursive: true });
    writeFileSync(target, readFileSync(source));
  }
  console.log(`Generated ${pairs.length} files; updated ${changed.length}.`);
} else if (changed.length > 0) {
  throw new Error(`Generated files differ: ${changed.map(([, target]) => relative(root, target)).join(', ')}`);
} else {
  console.log(`Generated ${pairs.length} files match the contract.`);
}
