(() => {
  const root = document.documentElement;
  const store = (key, value) => {
    try {
      localStorage.setItem(key, value);
    } catch {}
  };

  const toggle = document.querySelector('[data-theme-toggle]');
  toggle?.addEventListener('click', () => {
    const current = root.getAttribute('data-theme') || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    const next = current === 'dark' ? 'light' : 'dark';
    root.setAttribute('data-theme', next);
    store('amdl-site-theme', next);
  });

  for (const button of document.querySelectorAll('[data-copy]')) {
    const label = button.getAttribute('aria-label');
    let timer;
    button.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(button.dataset.copy);
      } catch {
        return;
      }
      button.classList.add('is-done');
      button.setAttribute('aria-label', button.dataset.copied);
      clearTimeout(timer);
      timer = setTimeout(() => {
        button.classList.remove('is-done');
        button.setAttribute('aria-label', label);
      }, 1600);
    });
  }

  // The page draws each tab list with its first tab open (blocks.mjs#tabs).
  for (const list of document.querySelectorAll('[role="tablist"]')) {
    const tabs = [...list.querySelectorAll('[role="tab"]')];
    const select = (tab, focus) => {
      for (const t of tabs) {
        const on = t === tab;
        t.setAttribute('aria-selected', String(on));
        t.tabIndex = on ? 0 : -1;
        document.getElementById(t.getAttribute('aria-controls')).hidden = !on;
      }
      if (focus) tab.focus();
    };
    tabs.forEach((tab, i) => {
      tab.addEventListener('click', () => select(tab));
      tab.addEventListener('keydown', (event) => {
        const step = { ArrowRight: 1, ArrowLeft: -1 }[event.key];
        if (!step) return;
        event.preventDefault();
        select(tabs[(i + step + tabs.length) % tabs.length], true);
      });
    });
    // A link to a tab (#tab-…) opens it as well as scrolling to it.
    for (const tab of tabs) for (const a of document.querySelectorAll(`a[href="#${tab.id}"]`)) a.addEventListener('click', () => select(tab));
  }

  // Live demos. A box with data-scale draws its app that many px wide and
  // scales it to fit (--s, used by demo.css). A console loads as it nears the
  // screen and is told when it is off screen, which stops its jobs moving.
  for (const box of document.querySelectorAll('[data-scale]')) {
    new ResizeObserver(() => box.style.setProperty('--s', String(box.clientWidth / Number(box.dataset.scale)))).observe(box);
  }
  const shown = new WeakSet();
  const tell = (frame) => frame.contentWindow?.postMessage({ amdlDemoOffscreen: !shown.has(frame) }, location.origin);
  const near = new IntersectionObserver(
    (entries) => {
      for (const { target, isIntersecting } of entries) {
        if (!isIntersecting) continue;
        near.unobserve(target);
        target.src = target.dataset.src;
      }
    },
    { rootMargin: '600px 0px' },
  );
  const seen = new IntersectionObserver((entries) => {
    for (const { target, isIntersecting } of entries) {
      if (isIntersecting) shown.add(target);
      else shown.delete(target);
      tell(target);
    }
  });
  for (const frame of document.querySelectorAll('iframe[data-demo]')) {
    near.observe(frame);
    seen.observe(frame);
    frame.addEventListener('load', () => tell(frame));
  }

  // The TUI's web build loads in place of its frame when the visitor starts
  // it, as wide as the frame (or the box, if narrower) so it lays out the
  // same, and takes the keyboard.
  for (const start of document.querySelectorAll('.term-start')) {
    const box = start.parentElement;
    const frame = box.querySelector('iframe');
    start.addEventListener('click', () => {
      box.scrollLeft = 0;
      frame.style.width = `${box.querySelector('pre').offsetWidth}px`;
      frame.src = frame.dataset.src;
      box.classList.add('is-live');
      frame.focus();
    });
  }

  // The desktop app's web build loads when the visitor starts it, and wears
  // the site's theme through MyGo's data-mygo-theme.
  const dark = matchMedia('(prefers-color-scheme: dark)');
  for (const start of document.querySelectorAll('.win-start')) {
    const body = start.parentElement;
    const frame = body.querySelector('iframe');
    const theme = () => {
      try {
        frame.contentDocument.documentElement.dataset.mygoTheme = root.getAttribute('data-theme') || (dark.matches ? 'dark' : 'light');
      } catch {}
    };
    start.addEventListener('click', () => {
      frame.addEventListener('load', theme);
      frame.src = frame.dataset.src;
      body.classList.add('is-live');
      frame.focus();
    });
    new MutationObserver(theme).observe(root, { attributes: true, attributeFilter: ['data-theme'] });
    dark.addEventListener('change', theme);
  }
})();
