import { goEnv, run } from './quality-process.mjs';
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
const env = goEnv();

for (const test of [false, true]) {
  const graph = run('go', ['list', '-deps', ...flags, ...(test ? ['-test'] : []), ...packages], { env, capture: true });
  const hasWails = graph.split(/\r?\n/).includes('github.com/wailsapp/wails/v3/pkg/application');
  if (hasWails !== native) throw new Error(`Unexpected Wails application dependency in ${test ? 'test' : 'production'} graph`);
  console.log(`${test ? 'Test' : 'Production'} graph: Wails application ${hasWails ? 'required (native)' : 'absent'}`);
}
run('go', ['test', '-count=1', '-shuffle=on', '-timeout=240s', ...flags, ...packages], { env });
