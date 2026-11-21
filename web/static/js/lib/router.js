// Tiny history API router.
import { html, useEffect, useState } from '../../vendor/preact-htm.js';

const listeners = new Set();
const notify = () => listeners.forEach((f) => f());
window.addEventListener('popstate', notify);

export function navigate(to, { replace = false } = {}) {
  if (to === location.pathname + location.search) return;
  history[replace ? 'replaceState' : 'pushState'](null, '', to);
  notify();
  if (!replace) window.scrollTo({ top: 0 });
}

export function useLocation() {
  const read = () => ({ path: location.pathname, query: new URLSearchParams(location.search) });
  const [loc, setLoc] = useState(read);
  useEffect(() => {
    const f = () => setLoc(read());
    listeners.add(f);
    return () => listeners.delete(f);
  }, []);
  return loc;
}

// match('/certificates/:id', '/certificates/5') -> { id: '5' } | null
export function match(pattern, path) {
  const p = pattern.split('/').filter(Boolean);
  const s = path.split('/').filter(Boolean);
  if (p.length !== s.length) return null;
  const params = {};
  for (let i = 0; i < p.length; i++) {
    if (p[i].startsWith(':')) params[p[i].slice(1)] = decodeURIComponent(s[i]);
    else if (p[i] !== s[i]) return null;
  }
  return params;
}

export function Link({ href, children, class: cls, className, onClick, ...rest }) {
  const handle = (e) => {
    onClick?.(e);
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    navigate(href);
  };
  return html`<a href=${href} class=${cls || className} onClick=${handle} ...${rest}>${children}</a>`;
}

export function setQuery(params) {
  const q = new URLSearchParams(location.search);
  for (const [k, v] of Object.entries(params)) {
    if (v === '' || v === null || v === undefined) q.delete(k);
    else q.set(k, v);
  }
  const s = q.toString();
  history.replaceState(null, '', location.pathname + (s ? '?' + s : ''));
  notify();
}
