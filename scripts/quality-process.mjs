import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

export const root = fileURLToPath(new URL('..', import.meta.url));
export const portableGoPackages = ['./api/...', './cmd/...', './internal/...'];

export function goEnv() {
  const env = { ...process.env };
  for (const name of Object.keys(env)) {
    if (['GOOS', 'GOARCH', 'GOENV', 'GOFLAGS', 'GOWORK', 'GOTOOLCHAIN', 'GOPROXY', 'GONOPROXY'].includes(name.toUpperCase())) {
      delete env[name];
    }
  }
  // All checks use the prepared local toolchain and module cache, including child Go builds.
  return {
    ...env,
    GOENV: 'off',
    GOFLAGS: '-mod=readonly',
    GOWORK: 'off',
    GOTOOLCHAIN: 'local',
    GOPROXY: 'off',
    GONOPROXY: 'none',
  };
}

export function run(command, args, { cwd = root, env = process.env, capture = false } = {}) {
  console.log(`> ${command} ${args.join(' ')}`);
  const result = spawnSync(command, args, { cwd, env, encoding: 'utf8', stdio: capture ? 'pipe' : 'inherit' });
  if (result.error) throw new Error(`Cannot run ${command}: ${result.error.message}`, { cause: result.error });
  if (result.signal) throw new Error(`${command} terminated by signal ${result.signal}`);
  if (capture && result.stderr) process.stderr.write(result.stderr);
  if (result.status !== 0) {
    if (capture && result.stdout) process.stdout.write(result.stdout);
    process.exit(result.status ?? 1);
  }
  return result.stdout;
}
