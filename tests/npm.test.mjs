import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const root = fileURLToPath(new URL('../', import.meta.url));
const cli = path.join(root, 'npm/cli.mjs');
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
function fixture(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'filewise-npm-'));
  for (const name of ['home', 'project', 'bin', 'tools']) fs.mkdirSync(path.join(dir, name), { mode: 0o700 });
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const env = { ...process.env, HOME: path.join(dir, 'home') };
  delete env.FILEWISE_INSTALL_DIR; delete env.FILEWISE_TOKEN; delete env.FILEWISE_URL;
  return { dir, env, run(args, extra = {}) {
    return spawnSync(process.execPath, [cli, ...args], { cwd: path.join(dir, 'project'), env: { ...env, ...extra }, encoding: 'utf8', timeout: 30000 });
  }};
}
const skillFiles = ['SKILL.md', 'references/commands.md', 'references/setup.md'];

test('explicit providers, scopes, exact payload, repeat install and preservation', t => {
  const f = fixture(t);
  const args = ['skills', '--providers', 'codex,claude,cursor,codex'];
  const installed = f.run(args);
  assert.equal(installed.status, 0);
  assert.ok(installed.stdout.includes(path.join(fs.realpathSync(path.join(f.dir, 'project')), '.agents/skills/filewise/references/setup.md')));
  for (const dir of ['.agents', '.claude', '.cursor']) {
    for (const name of skillFiles) {
      assert.deepEqual(fs.readFileSync(path.join(f.dir, 'project', dir, 'skills/filewise', name)), fs.readFileSync(path.join(root, 'skills/filewise', name)));
    }
  }
  assert.match(f.run(args).stdout, /Unchanged codex/);
  assert.equal(f.run(['skills', '--providers', 'codex', '--scope', 'user']).status, 0);
  assert.ok(fs.existsSync(path.join(f.dir, 'home/.agents/skills/filewise/SKILL.md')));
  const spaced = path.join(f.dir, 'project with spaces'); fs.mkdirSync(spaced);
  assert.equal(f.run(['skills', '--providers', 'cursor', '--dir', spaced]).status, 0);
  assert.ok(fs.existsSync(path.join(spaced, '.cursor/skills/filewise/SKILL.md')));
  const modified = path.join(f.dir, 'project/.agents/skills/filewise/SKILL.md');
  fs.appendFileSync(modified, '\nHuman customization\n');
  const before = fs.readFileSync(modified);
  const failed = f.run(['install', '--providers', 'codex']);
  assert.equal(failed.status, 1);
  assert.match(failed.stderr, /Existing skill differs/);
  assert.deepEqual(fs.readFileSync(modified), before);
  assert.equal(fs.existsSync(path.join(f.dir, 'home/.local')), false);
});

test('invalid arguments fail before writes or downloads', t => {
  const f = fixture(t);
  for (const args of [
    ['skills'], ['install', '--providers', 'unknown'], ['install', '--providers', 'codex,'],
    ['skills', '--providers', 'codex', '--scope', 'system'],
    ['skills', '--providers', 'codex', '--scope', 'user', '--dir', '.'],
    ['install', '--providers', 'codex', '--native-version', '../latest'],
    ['install', '--providers', 'codex', '--bin-dir', 'relative'],
    ['skills', '--providers', 'codex', '--force'], ['install', 'extra']
  ]) assert.equal(f.run(args).status, 1, JSON.stringify(args));
  assert.deepEqual(fs.readdirSync(path.join(f.dir, 'project')), []);
  assert.deepEqual(fs.readdirSync(path.join(f.dir, 'home')), []);
  assert.equal(f.run(['--help']).status, 0);
  assert.equal(f.run(['--version']).stdout.trim(), `filewise installer ${pkg.version}`);
});

test('symlink, unsafe parent, changed file and cross-provider preflight', t => {
  const f = fixture(t);
  const project = path.join(f.dir, 'project');
  fs.symlinkSync(path.join(f.dir, 'home'), path.join(project, '.claude'));
  let result = f.run(['skills', '--providers', 'codex,claude']);
  assert.equal(result.status, 1); assert.match(result.stderr, /symlink/);
  assert.equal(fs.existsSync(path.join(project, '.agents')), false);
  fs.unlinkSync(path.join(project, '.claude'));
  fs.mkdirSync(path.join(project, '.claude'), { mode: 0o777 });
  fs.chmodSync(path.join(project, '.claude'), 0o777);
  assert.equal(f.run(['skills', '--providers', 'claude']).status, 1);
  assert.equal(f.run(['skills', '--providers', 'codex']).status, 0);
  const setup = path.join(project, '.agents/skills/filewise/references/setup.md');
  fs.unlinkSync(setup);
  fs.symlinkSync(path.join(root, 'skills/filewise/references/setup.md'), setup);
  assert.equal(f.run(['skills', '--providers', 'codex']).status, 1);
});

test('installer delegates pinned Go version to Bash; failure keeps skills untouched', t => {
  const f = fixture(t);
  const script = path.join(f.dir, 'tools/bash');
  const log = path.join(f.dir, 'invocation');
  fs.writeFileSync(script, '#!/bin/sh\nprintf "%s\\n" "$@" "$FILEWISE_INSTALL_DIR" "${BASH_ENV-unset}" > "$TEST_LOG"\nexit 9\n', { mode: 0o755 });
  const env = { PATH: `${path.dirname(script)}:${process.env.PATH}`, TEST_LOG: log, BASH_ENV: '/untrusted/env' };
  const args = ['install', '--providers', 'codex', '--bin-dir', path.join(f.dir, 'bin')];
  const failed = f.run(args, env);
  assert.equal(failed.status, 1);
  assert.deepEqual(fs.readFileSync(log, 'utf8').trim().split('\n'), [path.join(root, 'install.sh'), pkg.filewise.nativeVersion, path.join(f.dir, 'bin'), 'unset']);
  assert.equal(fs.existsSync(path.join(f.dir, 'project/.agents')), false);
  fs.writeFileSync(script, '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  assert.equal(f.run(args, env).status, 0);
  fs.symlinkSync('/does-not-exist', path.join(f.dir, 'bin/filewise'));
  assert.match(f.run(args, env).stderr, /Refusing binary destination/);
});

test('doctor never prints the token and needs a real native version response', t => {
  const f = fixture(t);
  const binary = path.join(f.dir, 'bin/filewise');
  fs.writeFileSync(binary, `#!/bin/sh\nprintf "filewise ${pkg.version} (Go)\\n"\n`, { mode: 0o755 });
  const secret = 'synthetic-secret-not-a-real-token';
  const args = ['doctor', '--bin-dir', path.dirname(binary)];
  const result = f.run(args, { FILEWISE_TOKEN: secret });
  assert.equal(result.status, 0);
  assert.equal(JSON.parse(result.stdout).project_credential_present, true);
  assert.equal((result.stdout + result.stderr).includes(secret), false);
  for (const response of ['filewise 0.2.0', 'some other tool']) {
    fs.writeFileSync(binary, `#!/bin/sh\nprintf "${response}\\n"\n`, { mode: 0o755 });
    assert.equal(f.run(args).status, 1);
  }
});

test('skill references and setup shell examples stay self-contained and parse', () => {
  for (const name of skillFiles) {
    const source = fs.readFileSync(path.join(root, 'skills/filewise', name), 'utf8');
    for (const [, block] of source.matchAll(/```bash\n([\s\S]*?)```/g)) {
      assert.equal(spawnSync('bash', ['-n'], { input: block }).status, 0, name);
    }
    for (const [, reference] of source.matchAll(/\]\(([^)]+)\)/g)) {
      if (reference.startsWith('https://')) continue;
      assert.ok(fs.existsSync(path.resolve(root, 'skills/filewise', path.dirname(name), reference)), reference);
    }
  }
});

test('npm pack exact allowlist and offline npx install from the packed artifact', { timeout: 60000 }, t => {
  const f = fixture(t);
  const config = path.join(f.dir, 'empty.npmrc'); fs.writeFileSync(config, '');
  const env = { ...f.env, npm_config_cache: path.join(f.dir, 'cache'), npm_config_userconfig: config,
    npm_config_globalconfig: config + '.global', npm_config_registry: 'https://registry.npmjs.org' };
  const packed = spawnSync('npm', ['pack', '--ignore-scripts', '--json', '--pack-destination', f.dir], { cwd: root, env, encoding: 'utf8', timeout: 30000 });
  assert.equal(packed.status, 0, packed.stderr);
  const [pack] = JSON.parse(packed.stdout);
  const allowed = ['README.md', 'README.en.md', 'package.json', 'install.sh', 'npm/cli.mjs', ...skillFiles.map(p => `skills/filewise/${p}`)];
  for (const file of pack.files) assert.ok(allowed.includes(file.path), `Unexpected package entry: ${file.path}`);
  for (const file of ['package.json', 'install.sh', 'npm/cli.mjs', ...skillFiles.map(p => `skills/filewise/${p}`)]) assert.ok(pack.files.some(f => f.path === file), file);
  const archive = path.join(f.dir, pack.filename);
  for (const file of pack.files) {
    const bytes = spawnSync('tar', ['-xOzf', archive, `package/${file.path}`], { maxBuffer: 1024 * 1024 });
    assert.equal(bytes.status, 0); assert.deepEqual(bytes.stdout, fs.readFileSync(path.join(root, file.path)));
  }
  const exec = spawnSync('npm', ['exec', '--offline', '--yes', '--package', archive, '--', 'filewise', 'skills', '--providers', 'codex,claude,cursor'], {
    cwd: path.join(f.dir, 'project'), env, encoding: 'utf8', timeout: 30000
  });
  assert.equal(exec.status, 0, exec.stderr);
  assert.deepEqual(fs.readFileSync(path.join(f.dir, 'project/.agents/skills/filewise/SKILL.md')), fs.readFileSync(path.join(root, 'skills/filewise/SKILL.md')));
  assert.equal(fs.existsSync(path.join(f.dir, 'home/.local')), false);
});
