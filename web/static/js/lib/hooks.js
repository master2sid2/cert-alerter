import { useCallback, useEffect, useRef, useState } from '../../vendor/preact-htm.js';

// useAsync loads data with fn and exposes { data, error, loading, reload, setData }.
export function useAsync(fn, deps = []) {
  const [state, setState] = useState({ data: undefined, error: null, loading: true });
  const seq = useRef(0);
  const run = useCallback(async (silent = false) => {
    const id = ++seq.current;
    if (!silent) setState((s) => ({ ...s, loading: true }));
    try {
      const data = await fn();
      if (id === seq.current) setState({ data, error: null, loading: false });
      return data;
    } catch (error) {
      if (id === seq.current) setState((s) => ({ ...s, error, loading: false }));
      return undefined;
    }
  }, deps); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    run();
  }, [run]);
  const setData = (updater) =>
    setState((s) => ({ ...s, data: typeof updater === 'function' ? updater(s.data) : updater }));
  return { ...state, reload: run, setData };
}

// useInterval calls fn every ms while the page is visible.
export function useInterval(fn, ms) {
  const saved = useRef(fn);
  saved.current = fn;
  useEffect(() => {
    if (!ms) return undefined;
    const id = setInterval(() => {
      if (document.visibilityState === 'visible') saved.current();
    }, ms);
    return () => clearInterval(id);
  }, [ms]);
}

export function useTitle(title, appName) {
  useEffect(() => {
    document.title = title ? `${title} · ${appName || 'Cert Alerter'}` : appName || 'Cert Alerter';
  }, [title, appName]);
}

// useKey registers a global keyboard shortcut (ignored while typing).
export function useKey(key, fn) {
  const saved = useRef(fn);
  saved.current = fn;
  useEffect(() => {
    const h = (e) => {
      const tag = (e.target.tagName || '').toLowerCase();
      if (['input', 'textarea', 'select'].includes(tag) || e.target.isContentEditable) return;
      if (e.key === key && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        saved.current(e);
      }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [key]);
}
