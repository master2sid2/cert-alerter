// Translations and locale aware formatting.
import { app, pref, setPref } from './store.js';
import en from '../i18n/en.js';

// Interface languages. To add one, create js/i18n/<code>.js with the same keys
// as en.js and register it here; the language switch lists every entry.
const dicts = { en };
export const languages = [
  { code: 'en', label: 'English' },
];

// The interface is English by default; the choice is remembered per browser.
export function detectLang() {
  const saved = pref('lang', '');
  return dicts[saved] ? saved : 'en';
}

export function setLang(code) {
  if (!dicts[code]) return;
  setPref('lang', code);
  document.documentElement.lang = code;
  app.set({ lang: code });
}

export const lang = () => app.get().lang;

function interpolate(s, params) {
  if (!params) return s;
  return s.replace(/\{(\w+)\}/g, (m, k) => (params[k] !== undefined && params[k] !== null ? String(params[k]) : m));
}

export function has(key) {
  return (dicts[lang()] || en)[key] !== undefined || en[key] !== undefined;
}

export function t(key, params) {
  const d = dicts[lang()] || en;
  const s = d[key] ?? en[key];
  if (s === undefined) {
    if (window.__CA_DEBUG) console.warn('missing translation', key);
    return key;
  }
  return interpolate(s, params);
}

const pluralRules = {};
function pluralCategory(n) {
  const l = lang();
  pluralRules[l] ||= new Intl.PluralRules(l);
  return pluralRules[l].select(n);
}

// tp('unit.days', 5) -> "5 days"; keys: name.one / name.few / name.many / name.other
export function tp(key, n, params = {}) {
  const d = dicts[lang()] || en;
  const cat = pluralCategory(n);
  const s = d[`${key}.${cat}`] ?? d[`${key}.other`] ?? d[`${key}.many`] ?? en[`${key}.${n === 1 ? 'one' : 'other'}`];
  if (s === undefined) return `${n} ${key}`;
  return interpolate(s, { n: formatNumber(n), ...params });
}

export function formatNumber(n) {
  return new Intl.NumberFormat(lang()).format(n);
}

const toDate = (v) => (v instanceof Date ? v : new Date(v));

export function fmtDate(v) {
  if (!v) return '—';
  return new Intl.DateTimeFormat(lang(), { year: 'numeric', month: 'short', day: 'numeric' }).format(toDate(v));
}

export function fmtDateTime(v) {
  if (!v) return '—';
  return new Intl.DateTimeFormat(lang(), {
    year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
  }).format(toDate(v));
}

// fmtDateISO renders a compact, unambiguous date (2026-11-05) in local time.
export function fmtDateISO(v) {
  if (!v) return '—';
  const d = toDate(v);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export function fmtTime(v) {
  if (!v) return '—';
  return new Intl.DateTimeFormat(lang(), { hour: '2-digit', minute: '2-digit' }).format(toDate(v));
}

const units = [
  ['year', 365 * 24 * 3600],
  ['month', 30 * 24 * 3600],
  ['week', 7 * 24 * 3600],
  ['day', 24 * 3600],
  ['hour', 3600],
  ['minute', 60],
  ['second', 1],
];

// fmtRelative(date) -> "5 minutes ago" / "in 3 days"; short: "5 min. ago"
export function fmtRelative(v, short = false) {
  if (!v) return '—';
  const diff = (toDate(v).getTime() - Date.now()) / 1000;
  const abs = Math.abs(diff);
  if (abs < 45) return t('time.just_now');
  const rtf = new Intl.RelativeTimeFormat(lang(), { numeric: 'auto', style: short ? 'short' : 'long' });
  for (const [unit, sec] of units) {
    if (abs >= sec || unit === 'second') return rtf.format(Math.round(diff / sec), unit);
  }
  return '';
}

export const fmtDays = (n) => tp('unit.days', n);

// fmtDaysLeft(5) -> "in 5 days"; (-3) -> "expired 2 days ago" (days are floored)
export function fmtDaysLeft(days) {
  if (days === null || days === undefined) return '—';
  if (days >= 0) return days === 0 ? t('days.today') : t('days.in', { days: fmtDays(days) });
  const ago = -days - 1;
  return ago === 0 ? t('days.expired_today') : t('days.expired_ago', { days: fmtDays(ago) });
}

export function thresholdLabel(th) {
  return th === 0 ? t('threshold.expired') : t('threshold.days', { days: fmtDays(th) });
}

// errorText localises API errors.
export function errorText(err) {
  if (!err) return '';
  if (err.code && has('error.' + err.code)) return t('error.' + err.code);
  return err.message || t('error.unknown');
}

export function fieldError(code) {
  if (!code) return '';
  return has('field.' + code) ? t('field.' + code) : code;
}
