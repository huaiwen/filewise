import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';
import { once } from 'node:events';
import { createHash } from 'node:crypto';
import { previewServer } from '../site/serve.mjs';

const root = fileURLToPath(new URL('../site/', import.meta.url));
const pages = ['index.html', 'en/index.html'];

test('both static pages have valid local links, tab targets and publication boundaries', () => {
  let native;
  for (const page of pages) {
    const source = fs.readFileSync(path.join(root, page), 'utf8');
    const ids = [...source.matchAll(/\bid="([^"]+)"/g)].map(m => m[1]);
    assert.equal(new Set(ids).size, ids.length, `duplicate IDs: ${page}`);
    assert.equal((source.match(/<h1[ >]/g) || []).length, 1);
    assert.match(source, /<html lang="(?:en|zh-CN)"/);
    assert.match(source, /connect-src 'none'/);
    for (const [, reference] of source.matchAll(/\b(?:href|src)="([^"]+)"/g)) {
      if (reference.startsWith('https://')) {
        assert.ok(reference.startsWith('https://github.com/huaiwen/filewise'), reference); continue;
      }
      if (reference.startsWith('#')) { assert.ok(ids.includes(reference.slice(1)), reference); continue; }
      const file = path.resolve(root, path.dirname(page), reference);
      assert.ok(file.startsWith(root) && fs.existsSync(file), reference);
    }
    for (const [, references] of source.matchAll(/aria-(?:controls|labelledby|describedby)="([^"]+)"/g)) {
      for (const id of references.split(/\s+/)) assert.ok(ids.includes(id), id);
    }
    assert.match(source, /font-src 'self'/);
    assert.match(source, /id="version-range" type="range" min="0" max="1" step="1" value="1" disabled/);
    assert.doesNotMatch(source, /class="eyebrow"|requirement\.json|pressure_kpa|class="code-view"/);
    assert.match(source, /word\/document\.xml\/paragraph:3/);
    assert.match(source, /META \/ (?:变动语义|CHANGE MEANING)/);
    assert.match(source, /Go (?:迁移|migration)/);
    assert.match(source, /v0\.2\.0 · Rust/);
    const locale = page.startsWith('en/') ? 'en' : 'zh';
    for (const version of ['before', 'after']) {
      const file = `examples/acceptance-${locale}-${version}.docx`;
      const bytes = fs.readFileSync(path.join(root, file));
      assert.equal(bytes.subarray(0, 4).toString('hex'), '504b0304');
      const sha = createHash('sha256').update(bytes).digest('hex');
      assert.ok(source.includes(`${sha.slice(0, 4)}…${sha.slice(-4)}`), 'demo hashes match downloadable Word originals');
      assert.ok(source.includes(`${file}" download`));
    }
    assert.equal((source.match(/role="tab"/g) || []).length, 7);
    assert.equal((source.match(/class="pending"/g) || []).length, 3);
    assert.equal((source.match(/data-copy=/g) || []).length, 1, 'Only the released native command may be copied');
    assert.match(source, /npm 包尚未发布|npm package is not published/);
    assert.match(source, /示例数据|sample data/i);
    assert.doesNotMatch(source, /<iframe|\son\w+="|fetch\(/);
    const command = source.match(/id="native-command">([\s\S]*?)<\/code>/)[1];
    if (native) assert.equal(command, native); else native = command;
    for (const provider of ['codex', 'claude', 'cursor']) assert.ok(source.includes(`npx filewise install --providers ${provider}`));
  }
});

test('tabs support pointer and keyboard; clipboard success and manual fallback', async () => {
  const range = { value: '1', disabled: true, dataset: { before: 'Previous version: 100 kPa', after: 'Current version: 120 kPa' }, handlers: {}, attributes: {},
    addEventListener(key, fn) { this.handlers[key] = fn; }, setAttribute(key, value) { this.attributes[key] = value; } };
  const targets = { a: { hidden: false }, b: { hidden: true }, command: { textContent: '  native command  ' }, 'copy-status': { textContent: '' },
    'version-range': range, 'version-before': { hidden: true }, 'version-after': { hidden: false } };
  const tab = (name, selected) => ({ attributes: { 'aria-controls': name, 'aria-selected': selected }, handlers: {},
    getAttribute(key) { return this.attributes[key]; }, setAttribute(key, value) { this.attributes[key] = value; },
    addEventListener(key, handler) { this.handlers[key] = handler; }, focus() { this.focused = true; } });
  const tabs = [tab('a', 'true'), tab('b', 'false')];
  const button = { dataset: { copy: 'command' }, handlers: {}, addEventListener(key, fn) { this.handlers[key] = fn; } };
  let copied, selected, refuse = false;
  vm.runInNewContext(fs.readFileSync(path.join(root, 'app.js'), 'utf8'), {
    document: {
      documentElement: { lang: 'en' },
      querySelectorAll(selector) { return selector === '[data-tabs]' ? [{ querySelectorAll: () => tabs }] : [button]; },
      getElementById: id => targets[id], createRange: () => ({ selectNodeContents: element => { selected = element; } })
    },
    navigator: { clipboard: { async writeText(text) { if (refuse) throw Error('denied'); copied = text; } } },
    window: { getSelection: () => ({ removeAllRanges() {}, addRange() {} }) }
  });
  assert.equal(range.disabled, false);
  assert.equal(targets['version-after'].hidden, false);
  assert.equal(range.attributes['aria-valuetext'], range.dataset.after);
  range.value = '0'; range.handlers.input();
  assert.equal(targets['version-before'].hidden, false); assert.equal(targets['version-after'].hidden, true);
  assert.equal(range.attributes['aria-valuetext'], range.dataset.before);
  range.value = '1'; range.handlers.input();
  assert.equal(targets['version-before'].hidden, true); assert.equal(targets['version-after'].hidden, false);
  tabs[1].handlers.click(); assert.equal(targets.a.hidden, true); assert.equal(targets.b.hidden, false);
  tabs[1].handlers.keydown({ key: 'ArrowRight', preventDefault() {} });
  assert.equal(targets.a.hidden, false); assert.equal(tabs[0].focused, true);
  assert.equal(tabs[1].tabIndex, -1);
  tabs[0].handlers.keydown({ key: 'End', preventDefault() {} }); assert.equal(targets.b.hidden, false);
  await button.handlers.click(); assert.equal(copied, 'native command'); assert.match(targets['copy-status'].textContent, /copied/);
  refuse = true; await button.handlers.click(); assert.equal(selected, targets.command); assert.match(targets['copy-status'].textContent, /manually/);
});

test('preview only serves allowlisted assets and never repository or state files', async () => {
  const server = previewServer(); server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const origin = `http://127.0.0.1:${server.address().port}`;
  try {
    for (const route of ['/', '/en/', '/style.css', '/app.js', '/favicon.svg', '/fonts/barlow-condensed-latin-300.woff2', '/fonts/noto-sans-sc-headings-300.woff2', '/fonts/BarlowCondensed-OFL.txt', '/fonts/NotoSansSC-OFL.txt', ...['zh', 'en'].flatMap(locale => ['before', 'after'].map(version => `/examples/acceptance-${locale}-${version}.docx`))]) {
      const response = await fetch(origin + route); assert.equal(response.status, 200, route);
      assert.match(response.headers.get('content-security-policy'), /connect-src 'none'/);
      assert.match(response.headers.get('content-security-policy'), /font-src 'self'/);
      if (route.endsWith('.docx')) {
        assert.equal(response.headers.get('content-type'), 'application/vnd.openxmlformats-officedocument.wordprocessingml.document');
        const bytes = Buffer.from(await response.arrayBuffer());
        assert.deepEqual(bytes, fs.readFileSync(path.join(root, route.slice(1))));
      }
      if (route.endsWith('.woff2')) {
        assert.equal(response.headers.get('content-type'), 'font/woff2');
        const bytes = Buffer.from(await response.arrayBuffer());
        assert.equal(bytes.subarray(0, 4).toString(), 'wOF2');
        assert.ok(bytes.length < 20000, 'Keep each bundled font bounded');
      }
    }
    for (const route of ['/package.json', '/serve.mjs', '/README.md', '/BRIEF.md', '/PRODUCT.md', '/DESIGN.md', '/.impeccable/design.json', '/fonts/README.md', '/examples/private.docx', '/go.mod', '/internal/journal/journal.go', '/.git/config', '/__proto__', '/../../README.md', '/%2e%2e/.env']) {
      assert.equal((await fetch(origin + route)).status, 404, route);
    }
    assert.equal((await fetch(origin, { method: 'POST' })).status, 405);
    const head = await fetch(origin, { method: 'HEAD' }); assert.equal(head.status, 200); assert.equal(await head.text(), '');
  } finally { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
});
