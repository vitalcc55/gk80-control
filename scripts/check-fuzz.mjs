import { cpSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { goEnv, root, run } from './quality-process.mjs';

if (process.argv.length !== 2) throw new Error('Usage: check-fuzz.mjs');
const artifacts = join(root, '.tmp', '.codex');
mkdirSync(artifacts, { recursive: true });
const copy = mkdtempSync(join(artifacts, 'fuzz-'));
for (const path of ['api', 'internal/contract', 'go.mod', 'go.sum']) {
  cpSync(join(root, path), join(copy, path), { recursive: true });
}
// Go writes failure inputs beside the package. Keep the isolated copy on failure for replay.
console.log(`Fuzz workspace (retained on failure): ${copy}`);
const env = goEnv();
const targets = ['FuzzCanonicalJSONNumber', 'FuzzInspectStartCommand'];
const listed = run('go', ['test', '-list', `^(${targets.join('|')})$`, './internal/contract'],
  { cwd: copy, env, capture: true }).split(/\r?\n/);
for (const target of targets) {
  if (!listed.includes(target)) throw new Error(`Fuzz target not found: ${target}`);
}
for (const target of targets) {
  run('go', ['test', '-count=1', '-run=^$', `-fuzz=^${target}$`, '-fuzztime=10s',
    '-fuzzminimizetime=3s', '-parallel=2', '-timeout=60s', './internal/contract',
  ], { cwd: copy, env });
}
rmSync(copy, { recursive: true, force: true, maxRetries: 3 });
