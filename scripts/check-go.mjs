import { copyFileSync, mkdirSync, mkdtempSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { goEnv, root, run } from './quality-process.mjs';

const args = process.argv.slice(2);
const native = args[0] === '--native';
if (args.length !== (native ? 1 : 0)) throw new Error('Usage: check-go.mjs [--native]');
if (native && process.platform !== 'win32') throw new Error('Native Go checks require Windows');
if (!['win32', 'linux'].includes(process.platform)) throw new Error('Go checks support Windows and Linux');

const env = goEnv();
const testFlags = ['-count=1', '-shuffle=on', '-timeout=240s'];

if (native) {
  const npm = process.env.npm_execpath;
  if (!npm) throw new Error('Run the native profile with npm run check:windows');
  run('powershell.exe', ['-NoProfile', '-File', 'scripts/build-desktop.ps1'], { env });
  run('golangci-lint', ['run', '--no-config', '--build-tags=desktopapp', '.', './internal/desktop'], { env });
  run('go', ['test', ...testFlags, '-tags=desktopapp', '.'], { env });

  const artifacts = join(root, '.tmp', '.codex');
  mkdirSync(artifacts, { recursive: true });
  const browser = mkdtempSync(join(artifacts, 'browser-build-'));
  const assets = join(browser, 'browser-assets');
  run(process.execPath, [npm, 'run', 'build:browser', '--', '--outDir', assets], { env });
  copyFileSync(join(assets, 'browser.html'), join(assets, 'index.html'));
  copyFileSync(join(root, 'bin', 'gk80-core.exe'), join(browser, 'gk80-core.exe'));
  run('go', ['build', '-o', join(browser, 'gk80-browser.exe'), './cmd/gk80-browser'], { env });
} else {
  const rootGoFiles = readdirSync(root, { withFileTypes: true })
    .filter(file => file.isFile() && file.name.endsWith('.go'))
    .map(file => join('.', file.name));
  const unformatted = run('gofmt', ['-l', 'api', 'cmd', 'internal', ...rootGoFiles], { capture: true }).trim();
  if (unformatted) throw new Error(`Go formatting differs (gofmt -l):\n${unformatted}`);

  const packages = ['./api/...', './cmd/...', './internal/...'];
  run('go', ['mod', 'tidy', '-diff'], { env });
  run('go', ['mod', 'verify'], { env });
  run('golangci-lint', ['run', '--no-config', ...packages], { env });
  run('go', ['test', ...testFlags, ...packages], { env });
  // Linux tests the shared browser code, but its executable entry point is Windows-only.
  run('go', ['build', './api/...', './internal/...', './cmd/gk80-core'], { env });
}
