// Local preview only. Deploy the allowlisted static assets; no server runtime is required.
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import path from 'node:path';

const root = fileURLToPath(new URL('./', import.meta.url));
const files = {
  '/': ['index.html', 'text/html; charset=utf-8'],
  '/index.html': ['index.html', 'text/html; charset=utf-8'],
  '/en/': ['en/index.html', 'text/html; charset=utf-8'],
  '/en/index.html': ['en/index.html', 'text/html; charset=utf-8'],
  '/style.css': ['style.css', 'text/css; charset=utf-8'],
  '/app.js': ['app.js', 'text/javascript; charset=utf-8'],
  '/favicon.svg': ['favicon.svg', 'image/svg+xml'],
  '/examples/acceptance-zh-before.docx': ['examples/acceptance-zh-before.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'],
  '/examples/acceptance-zh-after.docx': ['examples/acceptance-zh-after.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'],
  '/examples/acceptance-en-before.docx': ['examples/acceptance-en-before.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'],
  '/examples/acceptance-en-after.docx': ['examples/acceptance-en-after.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'],
  '/fonts/barlow-condensed-latin-300.woff2': ['fonts/barlow-condensed-latin-300.woff2', 'font/woff2'],
  '/fonts/noto-sans-sc-headings-300.woff2': ['fonts/noto-sans-sc-headings-300.woff2', 'font/woff2'],
  '/fonts/BarlowCondensed-OFL.txt': ['fonts/BarlowCondensed-OFL.txt', 'text/plain; charset=utf-8'],
  '/fonts/NotoSansSC-OFL.txt': ['fonts/NotoSansSC-OFL.txt', 'text/plain; charset=utf-8']
};
export function previewServer() {
  return createServer(async (req, res) => {
    res.setHeader('X-Content-Type-Options', 'nosniff');
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('Referrer-Policy', 'no-referrer');
    res.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'");
    if (!['GET', 'HEAD'].includes(req.method)) { res.writeHead(405, { Allow: 'GET, HEAD' }).end(); return; }
    try {
      const pathname = new URL(req.url, 'http://127.0.0.1').pathname;
      const entry = Object.hasOwn(files, pathname) ? files[pathname] : null;
      if (!entry) { res.writeHead(404).end('Not found'); return; }
      const body = await readFile(path.join(root, entry[0]));
      res.writeHead(200, { 'Content-Type': entry[1], 'Content-Length': body.length });
      res.end(req.method === 'HEAD' ? undefined : body);
    } catch { res.writeHead(500).end('Preview unavailable'); }
  });
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const { values } = parseArgs({ options: { port: { type: 'string', default: '8787' } } });
  const port = Number(values.port);
  if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error('Invalid port');
  const server = previewServer();
  server.on('error', error => { console.error(error.message); process.exitCode = 1; });
  server.listen(port, '127.0.0.1', () => console.log(`Filewise website: http://127.0.0.1:${server.address().port}`));
  for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => server.close());
}
