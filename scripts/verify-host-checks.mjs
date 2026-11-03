import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFileSync, cpSync, mkdirSync, mkdtempSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');

test('host profile reports a real failing Go test despite inherited filter and workspace', () => {
  const scratch = join(root, '.tmp', '.codex');
  mkdirSync(scratch, { recursive: true });
  const copy = mkdtempSync(join(scratch, 'host-profile-failure-'));
  for (const path of ['api', 'internal', 'cmd', 'go.mod', 'go.sum']) {
    cpSync(join(root, path), join(copy, path), { recursive: true });
  }
  mkdirSync(join(copy, 'scripts'));
  copyFileSync(join(root, 'scripts', 'check-host.mjs'), join(copy, 'scripts', 'check-host.mjs'));
  const marker = 'host profile must propagate this Go test failure';
  writeFileSync(join(copy, 'internal', 'desktop', 'host_profile_failure_test.go'),
    `package desktop\nimport "testing"\nfunc TestHostProfileFailure(t *testing.T) { t.Fatal(${JSON.stringify(marker)}) }\n`);
  const workspace = join(copy, 'original.go.work');
  writeFileSync(workspace, `go 1.27.3\nuse ${JSON.stringify(root.replaceAll('\\', '/'))}\n`);
  const result = spawnSync(process.execPath, ['scripts/check-host.mjs'], {
    cwd: copy,
    env: { ...process.env, GOFLAGS: '-run=^$', GOWORK: workspace },
    encoding: 'utf8',
    timeout: 240000,
  });
  assert.equal(result.error, undefined, String(result.error));
  assert.equal(result.signal, null);
  assert.equal(typeof result.status, 'number');
  assert.notEqual(result.status, 0, 'profile must fail');
  assert.match(`${result.stdout}\n${result.stderr}`, new RegExp(marker));
});

test('host profile fails offline with an unprepared module cache despite direct-download settings', () => {
  const scratch = join(root, '.tmp', '.codex');
  mkdirSync(scratch, { recursive: true });
  const cache = mkdtempSync(join(scratch, 'host-module-cache-'));
  const result = spawnSync(process.execPath, ['scripts/check-host.mjs'], {
    cwd: root,
    env: { ...process.env, GOMODCACHE: cache, GONOPROXY: '*', GOPRIVATE: '*' },
    encoding: 'utf8',
    timeout: 30000,
  });
  assert.equal(result.error, undefined, String(result.error));
  assert.equal(result.signal, null);
  assert.equal(typeof result.status, 'number');
  assert.notEqual(result.status, 0, 'unprepared cache must fail');
  assert.match(`${result.stdout}\n${result.stderr}`, /module lookup disabled by GOPROXY=off/);
  assert.equal(readdirSync(cache, { recursive: true }).some(path => path.endsWith('.zip')), false);
});
