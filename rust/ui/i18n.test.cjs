// Run with: node rust/ui/i18n.test.cjs (test tooling only; the application needs no Node runtime).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const i18n = require('./i18n.js');
const messages = JSON.parse(fs.readFileSync(path.join(__dirname,'strings.json')));
const keys = Object.keys(messages.en).sort();
assert.deepEqual(Object.keys(messages['zh-CN']).sort(),keys);
for(const key of keys) {
  for(const locale of Object.keys(messages)) assert.equal(typeof messages[locale][key],'string',key);
  const placeholders = text => [...text.matchAll(/\{(\w+)\}/g)].map(m=>m[1]).sort();
  assert.deepEqual(placeholders(messages.en[key]),placeholders(messages['zh-CN'][key]),key);
}
for(const [,key] of fs.readFileSync(path.join(__dirname,'index.html'),'utf8').matchAll(/data-i18n(?:-placeholder|-aria-label)?="([^"]+)"/g)) assert.ok(keys.includes(key),key);
assert.equal(i18n.resolve('auto',['zh-Hans-CN','en']), 'zh-CN');
assert.equal(i18n.resolve('auto',['en-GB','zh']), 'en');
assert.equal(i18n.resolve('auto',['de-DE']), 'en');
assert.equal(i18n.resolve('en',['zh-CN']), 'en');
assert.equal(i18n.resolve('zh-CN',['en']), 'zh-CN');
const en=i18n.create(messages,'en'), zh=i18n.create(messages,'zh-CN');
assert.equal(en.t('folder.monitored',{count:1}),'Monitoring · 1 file');
assert.equal(en.t('folder.monitored',{count:1000}),'Monitoring · 1,000 files');
assert.equal(zh.t('folder.monitored',{count:1}),'持续监控 · 1 文件');
assert.equal(zh.t('folder.monitored',{count:1000}),'持续监控 · 1,000 文件');
assert.equal(en.t('confirm.rename',{path:'<script>not executable</script>'}).includes('<script>not executable</script>'),true);
assert.equal(en.t('naming.hint').includes('{title}'),true);
assert.equal(i18n.create({en:{key:'Fallback'},'zh-CN':{}},'zh-CN').t('key'),'Fallback');
assert.equal(en.t('missing.key'),'missing.key');
for(const key of ['jobs.partial','coverage.extractive_text_partial','coverage.model_text_partial','error.document_extract','error.document_no_text','extraction.formulas_not_evaluated']) {
  assert.notEqual(en.t(key),key); assert.notEqual(zh.t(key),key); assert.notEqual(en.t(key),zh.t(key));
}
assert.equal(en.t('extraction.page_no_text',{page:'2'}),'Page 2: no text extracted; may need OCR.');
assert.equal(zh.t('extraction.page_extraction_failed',{page:'2'}),'第 2 页：文本提取失败。');
assert.equal(en.date('invalid'),'');
assert.match(en.date('2026-09-10T12:00:00Z'),/2026/);
assert.notEqual(en.date('2026-09-10T12:00:00Z'),zh.date('2026-09-10T12:00:00Z'));
const element={dataset:{i18n:'hero.title'},textContent:''};
i18n.apply({querySelectorAll:selector=>selector==='[data-i18n]'?[element]:[]},en);
assert.equal(element.textContent,'File workspace');
console.log(JSON.stringify({locales:Object.keys(messages),keys:keys.length,parity:true,fallback:true,interpolation:true,plural_and_Intl:true}));
