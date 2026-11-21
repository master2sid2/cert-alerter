// Minimal global state, event bus and toasts.
import { useEffect, useReducer } from '../../vendor/preact-htm.js';

export function createStore(initial) {
  let state = initial;
  const subs = new Set();
  return {
    get: () => state,
    set(patch) {
      state = { ...state, ...(typeof patch === 'function' ? patch(state) : patch) };
      subs.forEach((f) => f(state));
    },
    subscribe(f) {
      subs.add(f);
      return () => subs.delete(f);
    },
  };
}

export function useStore(store) {
  const [, force] = useReducer((x) => x + 1, 0);
  useEffect(() => store.subscribe(force), [store]);
  return store.get();
}

// app: { me, lang, theme, ready }
export const app = createStore({ me: null, ready: false, lang: 'en', theme: 'light' });

const handlers = new Map();
export function on(name, fn) {
  if (!handlers.has(name)) handlers.set(name, new Set());
  handlers.get(name).add(fn);
  return () => handlers.get(name).delete(fn);
}
export function emit(name, payload) {
  handlers.get(name)?.forEach((fn) => fn(payload));
}

// ---- toasts ----
export const toasts = createStore({ items: [] });
let toastSeq = 0;
export function toast(message, { tone = 'ok', title = '', timeout = 4500 } = {}) {
  const id = ++toastSeq;
  toasts.set((s) => ({ items: [...s.items, { id, message, tone, title }] }));
  if (timeout) setTimeout(() => dismissToast(id), timeout);
  return id;
}
export function dismissToast(id) {
  toasts.set((s) => ({ items: s.items.filter((t) => t.id !== id) }));
}

// ---- preferences (localStorage may be unavailable) ----
export function pref(key, fallback) {
  try {
    const v = localStorage.getItem('ca.' + key);
    return v === null ? fallback : v;
  } catch {
    return fallback;
  }
}
export function setPref(key, value) {
  try {
    localStorage.setItem('ca.' + key, value);
  } catch {
    /* ignore */
  }
}
