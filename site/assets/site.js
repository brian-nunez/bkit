(() => {
  const root = document.documentElement;
  try {
    const saved = localStorage.getItem('bkit-theme');
    if (saved === 'dark' || saved === 'light') root.dataset.theme = saved;
  } catch {}

  document.addEventListener('DOMContentLoaded', () => {
    const toggle = document.querySelector('.theme-toggle');
    const system = matchMedia('(prefers-color-scheme: dark)');
    const dark = () => root.dataset.theme ? root.dataset.theme === 'dark' : system.matches;
    const sync = () => {
      toggle.setAttribute('aria-pressed', String(dark()));
      toggle.textContent = dark() ? 'Light theme' : 'Dark theme';
    };
    toggle.hidden = false;
    sync();
    system.addEventListener('change', sync);
    toggle.addEventListener('click', () => {
      root.dataset.theme = dark() ? 'light' : 'dark';
      try { localStorage.setItem('bkit-theme', root.dataset.theme); } catch {}
      sync();
    });

    const status = document.querySelector('[role="status"]');
    document.querySelectorAll('.copy-btn').forEach(button => {
      button.hidden = false;
      button.addEventListener('click', async () => {
        const text = button.closest('.codebox').querySelector('code').textContent;
        button.disabled = true;
        status.textContent = '';
        try {
          await navigator.clipboard.writeText(text);
          button.textContent = 'Copied';
          status.textContent = 'Copied to clipboard.';
        } catch {
          button.textContent = 'Select text';
          const range = document.createRange();
          range.selectNodeContents(button.closest('.codebox').querySelector('code'));
          const selection = window.getSelection();
          selection.removeAllRanges();
          selection.addRange(range);
          status.textContent = 'Copy unavailable. Text selected for manual copying.';
        } finally {
          button.disabled = false;
          setTimeout(() => { button.textContent = 'Copy'; }, 1500);
        }
      });
    });
  });
})();
