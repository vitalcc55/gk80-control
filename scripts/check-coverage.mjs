import { mkdirSync, mkdtempSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { goEnv, portableGoPackages, root, run } from './quality-process.mjs';

if (process.argv.length !== 2) throw new Error('Usage: check-coverage.mjs');
// Vitest can offer to install an absent provider. Checks require it from npm ci.
import.meta.resolve('@vitest/coverage-v8');

const scope = ['./internal/contract', './internal/core', './internal/demo'];
const artifacts = join(root, '.tmp', '.codex');
mkdirSync(artifacts, { recursive: true });
const report = mkdtempSync(join(artifacts, 'coverage-'));
const raw = join(report, 'go.coverprofile');
const env = goEnv();
console.log(`Coverage reports: ${report}`);
run('go', ['test', '-count=1', '-shuffle=on', '-timeout=240s', '-covermode=set',
  `-coverpkg=${scope.join(',')}`, `-coverprofile=${raw}`, ...portableGoPackages,
], { env });

const [mode, ...lines] = readFileSync(raw, 'utf8').trimEnd().split(/\r?\n/);
if (mode !== 'mode: set') throw new Error(`Unexpected Go coverage mode: ${mode}`);
const blocks = new Map();
for (const line of lines) {
  const match = /^(.+):(\d+\.\d+,\d+\.\d+) (\d+) ([01])$/.exec(line);
  if (!match) throw new Error(`Invalid Go coverage row: ${line}`);
  const [, file, range, count, hits] = match;
  if (file.endsWith('/internal/contract/models.gen.go')) continue;
  // With -coverpkg, each test binary can emit the same block. A hit in any binary counts once.
  const key = `${file}:${range}`;
  const previous = blocks.get(key);
  if (previous && previous.total !== Number(count)) throw new Error(`Conflicting Go coverage rows: ${key}`);
  blocks.set(key, { file, total: Number(count), hit: hits === '1' || previous?.hit === true });
}

const files = new Map();
let covered = 0;
let total = 0;
for (const block of blocks.values()) {
  const file = files.get(block.file) ?? { covered: 0, total: 0 };
  file.total += block.total;
  total += block.total;
  if (block.hit) {
    file.covered += block.total;
    covered += block.total;
  }
  files.set(block.file, file);
}
if (!total) throw new Error('Go coverage contains no production statements');
for (const [name, file] of files) {
  if (file.total) console.log(`${name} ${(100 * file.covered / file.total).toFixed(2)}% (${file.covered}/${file.total})`);
}
console.log(`Go statements: ${(100 * covered / total).toFixed(2)}% (${covered}/${total}); informational, no percentage gate`);

run(process.execPath, ['node_modules/vitest/vitest.mjs', 'run', '--coverage',
  '--coverage.reportsDirectory', join(report, 'frontend'),
], { env: { ...process.env, CI: 'true' } });
