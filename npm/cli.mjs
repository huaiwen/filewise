#!/usr/bin/env node
// Distribution only. All file, storage, parsing and gateway operations run in the native Go executable.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { spawnSync } from 'node:child_process';
import { createInterface } from 'node:readline/promises';

const root = fileURLToPath(new URL('../', import.meta.url));
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
const providers = { codex: '.agents', claude: '.claude', cursor: '.cursor' };
const payload = ['SKILL.md', 'references/commands.md', 'references/setup.md'];
const help = `Filewise installer ${pkg.version} · native ${pkg.filewise.nativeVersion}

Usage:
  npx filewise install --providers codex,claude,cursor
  npx filewise skills --providers codex --scope user
  npx filewise doctor

Commands:
  install    Install the verified native binary and selected skills
  skills     Install skills only; no binary download
  doctor     Check the binary, skill files and credential presence (no network)

Options:
  --providers LIST      codex,claude,cursor; prompted in interactive terminals
  --scope project|user  Default: project
  --dir PATH            Existing project directory; default: current directory
  --bin-dir PATH        Absolute binary directory; default: ~/.local/bin
  --native-version TAG  Default: ${pkg.filewise.nativeVersion}; accepts vX.Y.Z[-suffix]
  --help                Show help
  --version             Show installer version

No npm lifecycle hooks, sudo, shell-profile edits or service startup.
Existing different skills are preserved: back them up and remove them to reinstall.
The Filewise engine supports macOS and Linux; Node is only used by this installer.`;

function fail(message) { throw new Error(message); }
function stat(file) {
  try { return fs.lstatSync(file); } catch (e) { if (e.code === 'ENOENT') return null; throw e; }
}
function directory(file) {
  const s = stat(file);
  if (s && (!s.isDirectory() || s.isSymbolicLink())) fail(`Refusing non-directory or symlink: ${file}`);
  if (s && (s.mode & 0o022)) fail(`Directory is writable by others: ${file}`);
  return s;
}
// Canonicalize the explicitly chosen root; reject linked descendants in harness paths.
function parents(base, relative, create = false) {
  let current = base;
  directory(current);
  for (const part of relative.split('/')) {
    current = path.join(current, part);
    if (!directory(current) && create) fs.mkdirSync(current, { mode: 0o755 });
  }
  return current;
}
function checkSkill(base, provider) {
  const folder = `${providers[provider]}/skills/filewise`;
  const target = parents(base, folder);
  if (!stat(target)) return { provider, target, existing: false };
  for (const name of payload) {
    const parent = path.dirname(name);
    if (parent !== '.') parents(target, parent);
    const file = path.join(target, name);
    const s = stat(file);
    if (!s?.isFile() || s.isSymbolicLink() || s.nlink !== 1 || s.size > 128 * 1024 ||
        !fs.readFileSync(file).equals(fs.readFileSync(path.join(root, 'skills/filewise', name)))) {
      fail(`Existing skill differs; nothing will be overwritten: ${target}`);
    }
  }
  return { provider, target, existing: true };
}
function installSkill(base, item) {
  // Recheck after downloads/prompts; keep unrelated or edited files intact.
  item = checkSkill(base, item.provider);
  if (item.existing) return;
  const parent = parents(base, `${providers[item.provider]}/skills`, true);
  const stage = fs.mkdtempSync(path.join(parent, '.filewise-'));
  try {
    fs.mkdirSync(path.join(stage, 'references'));
    for (const name of payload) fs.copyFileSync(path.join(root, 'skills/filewise', name), path.join(stage, name), fs.constants.COPYFILE_EXCL);
    fs.chmodSync(stage, 0o755);
    if (stat(item.target)) fail(`Skill destination changed: ${item.target}`);
    fs.renameSync(stage, item.target);
  } finally {
    fs.rmSync(stage, { recursive: true, force: true });
  }
}
function nativeVersion(binary) {
  const s = stat(binary);
  if (!s?.isFile() || s.isSymbolicLink()) return null;
  const result = spawnSync(binary, ['--version'], { encoding: 'utf8', timeout: 5000, maxBuffer: 4096 });
  return result.status === 0 && /^filewise \d+\.\d+\.\d+(?:-[\w.-]+)? \(Go\)\s*$/.test(result.stdout) ? result.stdout.trim() : null;
}

async function main() {
  const { values: opts, positionals } = parseArgs({
    allowPositionals: true,
    options: {
      providers: { type: 'string' }, scope: { type: 'string', default: 'project' },
      dir: { type: 'string' }, 'bin-dir': { type: 'string' },
      'native-version': { type: 'string', default: pkg.filewise.nativeVersion },
      help: { type: 'boolean' }, version: { type: 'boolean' }
    }
  });
  if (opts.help || !positionals.length && !opts.version) { console.log(help); return; }
  if (opts.version) { console.log(`filewise installer ${pkg.version}`); return; }
  const [command] = positionals;
  if (positionals.length !== 1 || !['install', 'skills', 'doctor'].includes(command)) fail('Use install, skills or doctor. See --help.');
  if (!['project', 'user'].includes(opts.scope)) fail('--scope must be project or user.');
  if (opts.dir && opts.scope === 'user') fail('--dir only applies to project scope.');
  if (!/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$/.test(opts['native-version'])) fail('Invalid --native-version.');
  if (!process.env.HOME || !path.isAbsolute(process.env.HOME)) fail('HOME must be an absolute path.');
  const binDir = opts['bin-dir'] ?? process.env.FILEWISE_INSTALL_DIR ?? path.join(process.env.HOME, '.local/bin');
  if (!path.isAbsolute(binDir)) fail('--bin-dir must be absolute.');
  const base = fs.realpathSync(opts.scope === 'user' ? process.env.HOME : (opts.dir ?? process.cwd()));
  directory(base);
  const binary = path.join(binDir, 'filewise');
  if (command === 'doctor') {
    const engine = nativeVersion(binary);
    const skills = Object.fromEntries(Object.keys(providers).map(provider => {
      try { return [provider, checkSkill(base, provider).existing ? 'current' : 'missing']; }
      catch { return [provider, 'different or unsafe; preserved']; }
    }));
    console.log(JSON.stringify({ installer: pkg.version, binary, engine, scope: opts.scope, skills,
      project_credential_present: Boolean(process.env.FILEWISE_TOKEN),
      note: 'No service connection tested. Never share the browser operator token with an agent.' }, null, 2));
    if (!engine) process.exitCode = 1;
    return;
  }
  if (!opts.providers) {
    if (!process.stdin.isTTY || !process.stdout.isTTY) fail('Choose --providers codex,claude,cursor (no files changed).');
    const detected = Object.keys(providers).filter(p => stat(path.join(base, providers[p]))?.isDirectory());
    const fallback = detected.length ? detected.join(',') : 'codex';
    const prompt = createInterface({ input: process.stdin, output: process.stdout });
    try { opts.providers = (await prompt.question(`Platforms: codex,claude,cursor [${fallback}]: `)).trim() || fallback; }
    finally { prompt.close(); }
  }
  const selected = [...new Set(opts.providers.split(',').map(s => s.trim()))];
  if (!selected.length || selected.some(p => !Object.hasOwn(providers, p))) fail('Unknown provider. Choose codex,claude,cursor.');
  const skills = selected.map(p => checkSkill(base, p)); // conflicts fail before any download
  if (command === 'install') {
    if (!['darwin', 'linux'].includes(process.platform)) fail('Native Filewise supports macOS/Linux. Use a supported host.');
    if (stat(binary) && (!stat(binary).isFile() || stat(binary).isSymbolicLink())) fail(`Refusing binary destination: ${binary}`);
    const env = { ...process.env, FILEWISE_INSTALL_DIR: binDir };
    delete env.BASH_ENV; delete env.ENV;
    const result = spawnSync('bash', [path.join(root, 'install.sh'), opts['native-version']], { env, stdio: 'inherit' });
    if (result.error) throw result.error;
    if (result.status !== 0) fail('Native installation failed; skills were not changed.');
  }
  for (const item of skills) {
    installSkill(base, item);
    console.log(`${item.existing ? 'Unchanged' : 'Installed'} ${item.provider}: ${item.target}`);
  }
  console.log('\nReload your coding tool. Codex: $filewise init · Claude Code / Cursor: /filewise init');
  console.log(`Init uses a project-scoped gateway credential. Operator setup: ${path.join(skills[0].target, 'references/setup.md')}`);
  console.log(`Native executable: ${binary}`);
  if (opts['bin-dir'] || process.env.FILEWISE_INSTALL_DIR) console.log('Set FILEWISE_BIN to this absolute executable path in your agent environment.');
}

main().catch(error => { console.error(`Filewise: ${error.message}`); process.exitCode = 1; });
