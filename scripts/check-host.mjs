import { spawnSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const args = process.argv.slice(2);
if (args.length > 1 || (args.length === 1 && !['--race', '--native'].includes(args[0]))) {
  throw new Error('Usage: check-host.mjs [--race | --native]');
}
const native = args[0] === '--native';
if (!['win32', 'linux'].includes(process.platform) || (native && process.platform !== 'win32')) {
  throw new Error(native ? 'Native host checks require Windows' : 'Host checks support Windows and Linux');
}

const packages = native
  ? ['./internal/desktop']
  : ['./internal/desktop', './internal/browser', './cmd/gk80-browser'];
const flags = native ? ['-tags=desktopapp'] : args[0] === '--race' ? ['-race'] : [];
// Setup is separate; inherited test filters must not silently shrink this profile.
const env = { ...process.env, GOPROXY: 'off', GONOPROXY: 'none', GOWORK: 'off', GOTOOLCHAIN: 'local', GOFLAGS: '-mod=readonly' };
const go = process.platform === 'win32' ? 'go.exe' : 'go';

function run(args, capture = false) {
  console.log(`> ${go} ${args.join(' ')}`);
  const result = spawnSync(go, args, { cwd: root, env, encoding: 'utf8', stdio: capture ? 'pipe' : 'inherit' });
  if (result.error) throw result.error;
  if (capture && result.stderr) process.stderr.write(result.stderr);
  if (result.signal) throw new Error(`Go terminated by signal ${result.signal}`);
  if (result.status !== 0) process.exit(result.status ?? 1);
  return result.stdout;
}

for (const test of [false, true]) {
  const graph = run(['list', '-deps', ...flags, ...(test ? ['-test'] : []), ...packages], true);
  const hasWails = graph.split(/\r?\n/).includes('github.com/wailsapp/wails/v3/pkg/application');
  if (hasWails !== native) throw new Error(`Unexpected Wails application dependency in ${test ? 'test' : 'production'} graph`);
  console.log(`${test ? 'Test' : 'Production'} graph: Wails application ${hasWails ? 'required (native)' : 'absent'}`);
}
run(['test', '-count=1', '-timeout=240s', ...flags, ...packages]);
