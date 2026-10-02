import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';

const binary = process.env.FILEWISE_TEST_BINARY;
test('Skill commands round-trip through a native scoped gateway', { skip: !binary, timeout: 45000 }, async () => {
  assert.ok(path.isAbsolute(binary));
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'filewise-skill-'));
  const db = path.join(dir, 'state/filewise.db'), tokens = path.join(dir, 'state/tokens.json');
  let server;
  const run = (args, env = {}, expected = 0) => {
    const result = spawnSync(binary, args, { env: { ...process.env, HOME: dir, ...env }, encoding: 'utf8', timeout: 15000 });
    assert.equal(result.status, expected, result.stderr);
    return JSON.parse(result.stdout || result.stderr);
  };
  try {
    fs.mkdirSync(path.join(dir, 'files'));
    fs.mkdirSync(path.join(dir, 'other'));
    fs.writeFileSync(path.join(dir, 'files/notes.md'), 'Test pressure: 100 kPa.\n');
    fs.writeFileSync(path.join(dir, 'other/private.md'), 'Other project.\n');
    run(['auth-init', '--out', tokens]);
    run(['--db', db, 'project', 'add', path.join(dir, 'files'), '--id', 'demo', '--name', 'Skill demo']);
    run(['--db', db, 'project', 'add', path.join(dir, 'other'), '--id', 'other', '--name', 'Other']);
    run(['--db', db, 'project', 'sync', 'demo']);
    const reader = run(['--db', db, 'auth-agent', 'demo', '--tokens', tokens, '--read-only']).FILEWISE_TOKEN;
    const editor = run(['--db', db, 'auth-agent', 'demo', '--tokens', tokens]).FILEWISE_TOKEN;
    // Credentials remain only in this process and child environments, never in test output.
    server = spawn(binary, ['--db', db, 'serve', '--tokens', tokens, '--port', '0'], { stdio: ['ignore', 'ignore', 'pipe'] });
    const url = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(Error('Gateway did not start')), 10000);
      let buffer = '';
      server.stderr.on('data', chunk => {
        buffer += chunk.toString();
        const match = buffer.match(/Filewise Go listening on (http:\/\/127\.0\.0\.1:\d+)/);
        if (match) { clearTimeout(timer); resolve(match[1]); }
      });
      server.once('error', error => { clearTimeout(timer); reject(error); });
      server.once('exit', () => { clearTimeout(timer); reject(Error('Gateway exited during startup')); });
    });
    const env = { FILEWISE_URL: url, FILEWISE_TOKEN: reader };
    assert.deepEqual(run(['agent', 'projects'], env).map(p => p.id), ['demo']);
    const base = run(['agent', 'ls', 'demo'], env).version;
    assert.match(base, /^[a-f0-9]{64}$/);
    const read = run(['agent', 'read', 'demo', 'notes.md', '--version', base], env);
    assert.equal(read.version, base); assert.match(JSON.stringify(read), /100 kPa/);
    const search = run(['agent', 'search', 'demo', 'pressure', '--mode', 'hybrid'], env);
    assert.match(JSON.stringify(search), /notes\.md/);
    run(['agent', 'ls', 'other'], env, 1);
    const save = ['agent', 'write', 'demo', 'notes.md', '--base', base, '--content', 'Test pressure: 120 kPa.\n', '--require-pass', '-m', 'Authorized pressure update'];
    run([...save, '--request-id', 'reader-write'], env, 1);
    env.FILEWISE_TOKEN = editor;
    assert.equal(run([...save, '--request-id', 'preview', '--dry-run'], env).saved, false);
    assert.equal(fs.readFileSync(path.join(dir, 'files/notes.md'), 'utf8'), 'Test pressure: 100 kPa.\n');
    run([...save, '--request-id', 'preview'], env, 1); // changed dry_run cannot reuse the key
    const saved = run([...save, '--request-id', 'actual-save'], env);
    assert.equal(saved.saved, true); assert.equal(saved.published, false);
    assert.deepEqual(run([...save, '--request-id', 'actual-save'], env), saved);
    assert.match(JSON.stringify(run(['agent', 'read', 'demo', 'notes.md', '--version', base], env)), /100 kPa/);
    assert.equal(run(['agent', 'diff', 'demo', base, saved.version], env).summary.changed, 1);
    const checks = path.join(dir, 'checks.json'), result = path.join(dir, 'result.json');
    fs.writeFileSync(checks, JSON.stringify([{ id: 'pressure', object_id: 'result', field: 'pressure', expected: 120 }]));
    fs.writeFileSync(result, JSON.stringify({ pressure: 120 }));
    const task = run(['agent', 'compile', 'demo', '--goal', 'Check pressure', '--path', 'notes.md', '--checks', checks], env);
    assert.ok(task.task_id);
    assert.equal(run(['agent', 'verify', 'demo', '--phase', 'postflight', '--task-id', task.task_id, '--result', result], env).decision, 'PASS');
  } finally {
    if (server && server.exitCode === null) {
      const exited = once(server, 'exit'); server.kill('SIGTERM');
      const kill = setTimeout(() => server.kill('SIGKILL'), 5000);
      try { await exited; } finally { clearTimeout(kill); }
    }
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
