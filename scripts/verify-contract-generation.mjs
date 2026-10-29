import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');

test('one generation uses the new schema for both Go and Wails bindings', () => {
  const tempRoot = join(root, '.tmp', '.codex');
  mkdirSync(tempRoot, { recursive: true });
  const copy = mkdtempSync(join(tempRoot, 'contract-regression-'));
  for (const path of ['api', 'internal', 'src/generated', 'scripts', 'go.mod', 'go.sum']) {
    cpSync(join(root, path), join(copy, path), { recursive: true });
  }
  const schemaPath = join(copy, 'api', 'core.yaml');
  const schema = readFileSync(schemaPath, 'utf8');
  const marker = '        source: { type: string, enum: [simulator] }';
  assert.equal(schema.split(marker).length, 2);
  writeFileSync(schemaPath, schema.replace(marker, `${marker}\n        probe: { type: string, description: any operator }`));

  const result = spawnSync(process.execPath, ['scripts/contract-generation.mjs', '--write'], {
    cwd: copy,
    encoding: 'utf8',
    timeout: 120000,
  });
  assert.equal(result.status, 0, `${result.error ?? ''}\n${result.stdout}\n${result.stderr}`);
  const model = readFileSync(join(copy, 'internal', 'contract', 'models.gen.go'), 'utf8');
  const types = readFileSync(join(copy, 'src', 'generated', 'wails', 'github.com', 'vitalcc55', 'gk80-control', 'internal', 'contract', 'models.ts'), 'utf8');
  assert.match(model, /Probe\s+\*string/);
  assert.match(types, /"probe"\?: string \| null/);

  const withAny = readFileSync(schemaPath, 'utf8').replace(
    'probe: { type: string, description: any operator }',
    'probe: { type: string, x-go-type: any }',
  );
  writeFileSync(schemaPath, withAny);
  const rejected = spawnSync(process.execPath, ['scripts/contract-generation.mjs', '--write'], {
    cwd: copy,
    encoding: 'utf8',
    timeout: 120000,
  });
  assert.notEqual(rejected.status, 0, 'unchecked any must fail generation');
  assert.match(rejected.stderr, /noExplicitAny/);
});
