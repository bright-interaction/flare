// App theme: 'dark' (default) or 'light'. Applied to <html data-theme> and
// persisted to localStorage. The CSS light overrides live in app.css.
//
// Note: the app is a static SPA behind a strict CSP (no inline scripts), so the
// theme is applied on mount rather than before first paint. Default is dark, so
// only a light-mode user sees a brief dark flash on load.

type Theme = 'dark' | 'light';

let current = $state<Theme>('dark');

export const theme = {
  get value(): Theme {
    return current;
  }
};

function apply(t: Theme) {
  current = t;
  if (typeof document !== 'undefined') {
    document.documentElement.dataset.theme = t;
  }
  try {
    localStorage.setItem('flare-theme', t);
  } catch {
    // ignore (private mode / storage disabled)
  }
}

export function initTheme() {
  if (typeof document === 'undefined') return;
  let initial: Theme = 'dark';
  try {
    const saved = localStorage.getItem('flare-theme');
    if (saved === 'light' || saved === 'dark') {
      initial = saved;
    } else if (window.matchMedia?.('(prefers-color-scheme: light)').matches) {
      initial = 'light';
    }
  } catch {
    // ignore
  }
  apply(initial);
}

export function toggleTheme() {
  apply(current === 'dark' ? 'light' : 'dark');
}
