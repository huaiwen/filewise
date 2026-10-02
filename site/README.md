# Filewise product website

Static Chinese (`index.html`) and English (`en/index.html`) pages. Shared local CSS/JS, self-hosted fonts and an original SVG mark; no build, framework, CDN, analytics, cookies, API calls or document uploads. The illustrated Word/version/semantic-meta/citation flow is labeled sample data, not a live workspace. Four downloadable synthetic Word files back the paragraph quotations and displayed SHA-256 prefixes; Go tests verify their extraction and change meaning.

## Preview

From the Filewise repository root:

```bash
npm run site
# Optional port:
npm run site -- --port 8788
```

Open `http://127.0.0.1:8787/`; English is `/en/`. Stop with Ctrl+C. Node 22+ is only a development preview tool. The server exposes an exact asset allowlist, not the repository or runtime state. `index.html` can also be opened directly; clipboard permissions vary, with manual selection as fallback.

## Test

```bash
npm test
```

Checks cover local links, bilingual install-command parity, tab targets, version selection, keyboard behavior, clipboard fallback, publication labels, font responses and preview-server isolation. Browser QA covers both languages, 320–1440px layouts, actual range/tab/copy controls, script-unavailable fallback, reduced motion and axe accessibility findings. This is not exhaustive WCAG certification or physical-device testing.

## Deployment

Serve these thirteen files, preserving paths:

```text
index.html
en/index.html
style.css
app.js
favicon.svg
examples/acceptance-zh-before.docx
examples/acceptance-zh-after.docx
examples/acceptance-en-before.docx
examples/acceptance-en-after.docx
fonts/barlow-condensed-latin-300.woff2
fonts/noto-sans-sc-headings-300.woff2
fonts/BarlowCondensed-OFL.txt
fonts/NotoSansSC-OFL.txt
```

Use any HTTPS static host. The site works at a domain root or a subdirectory, including a repository-style Pages path. Do not publish the repository root, `.git`, `.impeccable`, `PRODUCT.md`, `DESIGN.md`, development READMEs, credentials, npm tarballs, preview server, tests or private documents. No production runtime is required.

After a domain and hosting target are approved:
1. Deploy the static files, check both languages and links on the final URL.
2. Add the confirmed canonical/alternate absolute URLs and sitemap. No domain is guessed in this source.
3. Keep CSP restrictive; the site has no need to contact local Filewise. Set `frame-ancestors 'none'` as an HTTP header if the host supports it (the HTML meta policy cannot enforce that directive).
4. npm is currently unpublished: retain the notices and leave its copy buttons absent. After an authorized, independently verified npm release, update both pages and the publication-state assertions in `tests/site.test.mjs` together.

No hosting account, DNS record or deployment is created automatically.

## Design

The owner approved a visual direction close to [Impeccable](https://impeccable.style/): neutral white/gray, light display typography, open whitespace, a working example on the left and a clear black installation action. Filewise green is reserved for evidence and selected states. The demonstration uses a native version range and file/diff/citation tabs. Word text has a serif document hierarchy and a separate semantic-meta region, including a review boundary for downstream impact. It does not contact the local engine. Installation remains the released Rust v0.2.0; Go workspace migration is not represented as complete.

The official Impeccable 4.4.0 Skill was read at repository commit `40f990fac1ada1be12ab8d4ae6a6abc6c75b7f4e`; its context loader, concept seed, surface-brief storage and mechanical detector were run. The owner's explicit direction overrides the random seed. The build and finish review were parent-performed, not delegated independent reviews. Official fonts/branding/testimonials/source were not copied from Impeccable. The two display fonts come from Google Fonts under OFL; sources and hashes are in [fonts/README.md](fonts/README.md).

The implemented system is documented in [DESIGN.md](../DESIGN.md); product facts in [PRODUCT.md](../PRODUCT.md); the development-only direction contract in [.impeccable/surfaces/site-index-html.md](../.impeccable/surfaces/site-index-html.md). Essential product and native-install content remains visible if JavaScript is unavailable. Fonts use local fallback stacks if loading fails.

## 中文

根目录运行 `npm run site`，打开 `http://127.0.0.1:8787/` 预览；英文入口为 `/en/`。网页为静态文件，部署只需要上述十三个文件（含四份合成 Word 示例、字体及其许可证）。复制安装命令、流程示例和平台切换在浏览器中运行，不访问本地 Filewise 服务或读取用户文件。

本目录是官网源码，尚未部署。域名与托管平台确定后再发布；npm 真实发布前保留对应提示。官网开发不修改原有文件工作台。
