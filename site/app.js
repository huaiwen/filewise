// Progressive enhancement: the default native install and full product copy work without JS.
for (const group of document.querySelectorAll('[data-tabs]')) {
  const tabs = [...group.querySelectorAll('[role="tab"]')];
  const activate = tab => {
    for (const item of tabs) {
      const selected = item === tab;
      item.setAttribute('aria-selected', String(selected));
      item.tabIndex = selected ? 0 : -1;
      document.getElementById(item.getAttribute('aria-controls')).hidden = !selected;
    }
  };
  for (const [index, tab] of tabs.entries()) {
    tab.addEventListener('click', () => activate(tab));
    tab.addEventListener('keydown', event => {
      const positions = { ArrowRight: (index + 1) % tabs.length, ArrowLeft: (index + tabs.length - 1) % tabs.length, Home: 0, End: tabs.length - 1 };
      if (!Object.hasOwn(positions, event.key)) return;
      event.preventDefault();
      const target = tabs[positions[event.key]];
      activate(target);
      target.focus();
    });
  }
}
const versionRange = document.getElementById('version-range');
if (versionRange) {
  const showVersion = () => {
    const before = versionRange.value === '0';
    document.getElementById('version-before').hidden = !before;
    document.getElementById('version-after').hidden = before;
    versionRange.setAttribute('aria-valuetext', before ? versionRange.dataset.before : versionRange.dataset.after);
  };
  versionRange.addEventListener('input', showVersion);
  versionRange.disabled = false;
  showVersion();
}
for (const button of document.querySelectorAll('[data-copy]')) {
  button.addEventListener('click', async () => {
    const command = document.getElementById(button.dataset.copy);
    const status = document.getElementById('copy-status');
    try {
      await navigator.clipboard.writeText(command.textContent.trim());
      status.textContent = document.documentElement.lang === 'zh-CN' ? '命令已复制。' : 'Command copied.';
    } catch {
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(command);
      selection.removeAllRanges();
      selection.addRange(range);
      status.textContent = document.documentElement.lang === 'zh-CN' ? '命令已选中，请手动复制。' : 'Command selected. Copy it manually.';
    }
  });
}
