import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { goEnv, portableGoPackages, root, run } from './quality-process.mjs';

if (process.argv.length !== 2 || !process.env.npm_execpath) {
  throw new Error('Usage: npm run check:deps');
}
if (!['win32', 'linux'].includes(process.platform)) throw new Error('Dependency scan supports Windows and Linux');

const env = goEnv();
const npm = process.env.npm_execpath;
const artifacts = join(root, '.tmp', '.codex');
mkdirSync(artifacts, { recursive: true });
const report = mkdtempSync(join(artifacts, 'dependencies-'));
const metadata = {
  timestamp: new Date().toISOString(),
  revision: run('git', ['rev-parse', 'HEAD'], { capture: true }).trim(),
  dirty: Boolean(run('git', ['status', '--porcelain'], { capture: true }).trim()),
  platform: `${process.platform}/${process.arch}`,
  node: process.version,
  npm: run(process.execPath, [npm, '--version'], { capture: true }).trim(),
};
console.log(`Dependency scan reports: ${report}`);

function scan(name, command, args, summarize) {
  console.log(`> ${command} ${args.join(' ')}`);
  const child = spawnSync(command, args, {
    cwd: root, env, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024,
  });
  writeFileSync(join(report, `${name}.stdout`), child.stdout ?? '');
  writeFileSync(join(report, `${name}.stderr`), child.stderr ?? '');
  const result = { name, args, exitCode: child.status, status: 'not checked' };
  try {
    if (child.error) throw child.error;
    if (child.signal) throw new Error(`terminated by ${child.signal}`);
    const summary = summarize(child.stdout, child.status);
    Object.assign(result, summary, {
      status: summary.findings.length ? 'checked, findings' : 'checked, no findings',
    });
  } catch (error) {
    result.error = error.message;
  }
  console.log(`${name}: ${result.status}${result.error ? ` (${result.error})` : ''}`);
  if (result.status !== 'checked, no findings') process.exitCode = 1;
  return result;
}

function summarizeGo(stdout, exitCode) {
  if (exitCode !== 0) throw new Error(`govulncheck exited ${exitCode}; see stderr`);
  // govulncheck encodes consecutive indented top-level objects, not JSON Lines.
  const messages = JSON.parse(`[${stdout.trim().replace(/^}\r?\n(?={)/gm, '},')}]`);
  const config = messages[0]?.config;
  if (config?.protocol_version !== 'v1.0.0' || config.scan_level !== 'symbol' ||
      !messages.some(message => message.SBOM?.roots?.length)) {
    throw new Error('No complete govulncheck source report');
  }
  const findings = new Map();
  const rank = ['module', 'package', 'symbol'];
  for (const { finding } of messages) {
    if (!finding) continue;
    const frame = finding.trace?.[0];
    if (!finding.osv || !frame?.module) throw new Error('Invalid govulncheck finding');
    const reachability = frame.function ? 'symbol' : frame.package ? 'package' : 'module';
    const key = `${finding.osv} ${frame.module} ${frame.version ?? ''}`;
    // A symbol trace is stronger evidence than a package or module match.
    if (rank.indexOf(reachability) > rank.indexOf(findings.get(key)?.reachability)) {
      findings.set(key, { advisory: finding.osv, reachability, ...frame, fixedVersion: finding.fixed_version });
    }
  }
  return { scope: 'Go production source graph, without tests or generator tools', config, findings: [...findings.values()] };
}

function summarizeNpm(stdout, exitCode) {
  const audit = JSON.parse(stdout);
  const count = audit.metadata?.vulnerabilities?.total;
  if (audit.error || audit.auditReportVersion !== 2 || !audit.vulnerabilities ||
      !Number.isInteger(count) || Object.keys(audit.vulnerabilities).length !== count ||
      exitCode !== (count > 0 ? 1 : 0)) {
    throw new Error(`No complete npm audit report (exit ${exitCode}); see stdout/stderr`);
  }
  if (!registryOrigin) throw new Error('Could not identify the configured npm advisory registry');
  if (existsSync(join(root, 'npm-shrinkwrap.json'))) {
    throw new Error('The npm profile expects package-lock.json, without npm-shrinkwrap.json');
  }
  const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
  const lock = JSON.parse(readFileSync(join(root, 'package-lock.json'), 'utf8')).packages;
  for (const key of ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies']) {
    const declared = manifest[key] ?? {};
    const locked = lock[''][key] ?? {};
    if (Object.keys(declared).length !== Object.keys(locked).length ||
        Object.entries(declared).some(([name, version]) => locked[name] !== version)) {
      throw new Error(`package.json and package-lock.json differ in ${key}`);
    }
  }
  const peers = Object.keys(manifest.peerDependencies ?? {});
  if (peers.some(name => (manifest.peerDependenciesMeta?.[name]?.optional === true) !==
      (lock[''].peerDependenciesMeta?.[name]?.optional === true))) {
    throw new Error('package.json and package-lock.json differ in optional peer dependencies');
  }
  const findings = Object.entries(audit.vulnerabilities).map(([name, advisory]) => ({
    name,
    severity: advisory.severity,
    paths: advisory.nodes.map(path => {
      const entry = lock[path];
      return {
        path, version: entry?.version,
        scope: !entry ? 'unknown' : entry.dev ? 'dev only' :
          entry.devOptional ? 'dev and optional non-dev' : 'non-dev path',
      };
    }),
  }));
  return {
    scope: 'npm lock graph: production, development, optional and peer dependencies',
    reachability: 'not analysed by npm audit',
    counts: audit.metadata.vulnerabilities,
    findings,
  };
}

const results = [scan('go-portable', 'govulncheck', ['-format=json', ...portableGoPackages], summarizeGo)];
if (process.platform === 'win32') {
  results.push(scan('go-windows', 'govulncheck', ['-format=json', '-tags=production,desktopapp', './...'], summarizeGo));
} else {
  console.log('go-windows: not checked (Windows only, outside the Linux profile)');
  results.push({ name: 'go-windows', status: 'not checked', applicable: false, reason: 'requires Windows' });
}
const registry = spawnSync(process.execPath, [npm, 'config', 'get', 'registry', '--prefix', root],
  { cwd: root, env, encoding: 'utf8' });
let registryOrigin = null;
try {
  if (registry.status === 0) registryOrigin = new URL(registry.stdout.trim()).origin;
} catch {
  // Keep the raw audit output, but do not report a result without registry provenance.
}
const npmResult = scan('npm', process.execPath, [npm, 'audit', '--json', '--prefix', root,
  '--package-lock=true', '--package-lock-only', '--global=false', '--workspaces=false',
  '--include=prod', '--include=dev', '--include=optional', '--include=peer',
  '--offline=false', '--audit-level=info', '--fetch-retries=0',
], summarizeNpm);
results.push({ ...npmResult, registryOrigin });
writeFileSync(join(report, 'summary.json'), JSON.stringify({ ...metadata, results }, null, 2) + '\n');
