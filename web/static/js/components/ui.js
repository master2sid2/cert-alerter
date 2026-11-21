// Reusable UI building blocks.
import { html, createPortal, useEffect, useLayoutEffect, useRef, useState, useId } from '../../vendor/preact-htm.js';
import { Icon } from './icons.js';
import { createStore, useStore, toasts, dismissToast, toast } from '../lib/store.js';
import { t, errorText, fieldError } from '../lib/i18n.js';

export function Button({ kind = '', size = '', icon, loading, children, class: cls = '', type = 'button', ...rest }) {
  const classes = ['btn', kind && `btn-${kind}`, size && `btn-${size}`, !children && icon ? 'btn-icon' : '', cls]
    .filter(Boolean).join(' ');
  return html`<button type=${type} class=${classes} disabled=${loading || rest.disabled} ...${rest}>
    ${loading ? html`<${Icon} name="refresh" class="spin" />` : icon ? html`<${Icon} name=${icon} />` : null}
    ${children}
  </button>`;
}

export function Spinner() {
  return html`<div class="spinner" role="status" aria-label=${t('common.loading')}></div>`;
}

export function PageLoader() {
  return html`<div class="center"><${Spinner} /></div>`;
}

export function ErrorState({ error, onRetry }) {
  return html`<div class="card"><div class="empty">
    <div class="art"><${Icon} name="alertCircle" size="48" /></div>
    <h3>${t('common.load_failed')}</h3>
    <p>${errorText(error)}</p>
    ${onRetry && html`<${Button} icon="refresh" onClick=${() => onRetry()}>${t('common.retry')}<//>`}
  </div></div>`;
}

export function Empty({ icon = 'inbox', title, children, action }) {
  return html`<div class="empty">
    <div class="art"><${EmptyArt} icon=${icon} /></div>
    ${title && html`<h3>${title}</h3>`}
    ${children && html`<p>${children}</p>`}
    ${action}
  </div>`;
}

function EmptyArt({ icon }) {
  return html`<svg viewBox="0 0 120 90" width="120" height="90" aria-hidden="true">
      <rect x="18" y="14" width="84" height="62" rx="12" fill="none" stroke="currentColor" stroke-opacity=".35" stroke-dasharray="4 5" />
      <circle cx="60" cy="45" r="21" fill="currentColor" fill-opacity=".08" />
    </svg>
    <span class="art-icon"><${Icon} name=${icon} size="24" /></span>`;
}

export function Skeleton({ rows = 5, height = 18 }) {
  return html`<div class="stack-sm" style=${{ padding: '18px' }}>
    ${Array.from({ length: rows }, (_, i) => html`<div class="skeleton" style=${{ height: height + 'px', width: `${92 - (i % 3) * 14}%` }}></div>`)}
  </div>`;
}

export function Card({ title, sub, tools, children, flush, class: cls = '', style }) {
  return html`<section class=${'card ' + cls} style=${style}>
    ${(title || tools) && html`<header class="card-head">
      <div class="grow"><h2>${title}</h2>${sub && html`<div class="sub">${sub}</div>`}</div>
      ${tools && html`<div class="tools">${tools}</div>`}
    </header>`}
    <div class=${flush ? 'card-body flush' : 'card-body'}>${children}</div>
  </section>`;
}

// ---- forms ---------------------------------------------------------------

export function Field({ label, hint, error, children, class: cls = '', htmlFor }) {
  return html`<div class=${'field ' + cls}>
    ${label && html`<label for=${htmlFor}>${label}</label>`}
    ${children}
    ${error ? html`<div class="error" role="alert">${fieldError(error)}</div>` : hint && html`<div class="hint">${hint}</div>`}
  </div>`;
}

export function TextField({ label, hint, error, value, onInput, class: cls, inputClass = '', textarea, ...rest }) {
  const id = useId();
  const props = {
    id, class: (textarea ? 'textarea ' : 'input ') + inputClass, value: value ?? '',
    'aria-invalid': error ? 'true' : undefined,
    onInput: (e) => onInput?.(e.target.value),
    ...rest,
  };
  return html`<${Field} label=${label} hint=${hint} error=${error} class=${cls} htmlFor=${id}>
    ${textarea ? html`<textarea ...${props}></textarea>` : html`<input ...${props} />`}
  <//>`;
}

export function SelectField({ label, hint, error, value, onChange, options, class: cls, ...rest }) {
  const id = useId();
  return html`<${Field} label=${label} hint=${hint} error=${error} class=${cls} htmlFor=${id}>
    <select id=${id} class="select" value=${value} onChange=${(e) => onChange?.(e.target.value)} ...${rest}>
      ${options.map((o) => html`<option value=${o.value}>${o.label}</option>`)}
    </select>
  <//>`;
}

export function NumberField({ value, onInput, ...rest }) {
  return html`<${TextField} type="number" inputmode="numeric" value=${value}
    onInput=${(v) => onInput(v === '' ? '' : Number(v))} ...${rest} />`;
}

export function Check({ checked, onChange, label, hint, disabled }) {
  return html`<label class="check">
    <input type="checkbox" checked=${checked} disabled=${disabled} onChange=${(e) => onChange(e.target.checked)} />
    <span class="box"><${Icon} name="check" /></span>
    <span class="text"><b>${label}</b>${hint && html`<small>${hint}</small>`}</span>
  </label>`;
}

export function Switch({ checked, onChange, label, hint, disabled }) {
  return html`<label class="switch">
    <input type="checkbox" role="switch" checked=${checked} disabled=${disabled} onChange=${(e) => onChange(e.target.checked)} />
    <span class="track"></span>
    <span class="check"><span class="text"><b>${label}</b>${hint && html`<small>${hint}</small>`}</span></span>
  </label>`;
}

export function Segmented({ value, onChange, options, label }) {
  return html`<div class="segmented" role="tablist" aria-label=${label}>
    ${options.map((o) => html`<button type="button" role="tab" aria-selected=${value === o.value}
      class=${value === o.value ? 'on' : ''} onClick=${() => onChange(o.value)}>
      ${o.icon && html`<${Icon} name=${o.icon} size="14" />`}${o.label}${o.count !== undefined && html`<span class="n">${o.count}</span>`}
    </button>`)}
  </div>`;
}

export function Tabs({ value, onChange, tabs }) {
  return html`<nav class="tabs" role="tablist">
    ${tabs.map((tb) => html`<button type="button" role="tab" aria-selected=${value === tb.value}
      class=${value === tb.value ? 'on' : ''} onClick=${() => onChange(tb.value)}>
      ${tb.icon && html`<${Icon} name=${tb.icon} />`}${tb.label}
    </button>`)}
  </nav>`;
}

export function FormError({ error }) {
  if (!error) return null;
  return html`<div class="callout tone-expired" role="alert"><${Icon} name="alertCircle" /><div class="grow">${errorText(error)}</div></div>`;
}

// ---- modal ---------------------------------------------------------------

export function Modal({ title, sub, icon, tone, onClose, children, footer, wide, onSubmit }) {
  const ref = useRef(null);
  useEffect(() => {
    const prev = document.activeElement;
    const h = (e) => { if (e.key === 'Escape') onClose?.(); };
    document.addEventListener('keydown', h);
    const first = ref.current?.querySelector('input:not([type=hidden]):not([disabled]), textarea, select');
    (first || ref.current)?.focus();
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', h);
      document.body.style.overflow = '';
      prev?.focus?.();
    };
  }, []);
  const content = html`
    <div class="modal-head">
      ${icon && html`<div class=${'modal-icon ' + (tone ? 'tone-' + tone : '')}><${Icon} name=${icon} /></div>`}
      <div class="grow"><h2>${title}</h2>${sub && html`<div class="sub">${sub}</div>`}</div>
      <${Button} kind="ghost" size="sm" icon="x" aria-label=${t('common.close')} onClick=${onClose} />
    </div>
    <div class="modal-body">${children}</div>
    ${footer && html`<div class="modal-foot">${footer}</div>`}`;
  return html`<div class="modal-root" onMouseDown=${(e) => { if (e.target === e.currentTarget) onClose?.(); }}>
    ${onSubmit
      ? html`<form ref=${ref} tabindex="-1" class=${'modal' + (wide ? ' wide' : '')} role="dialog" aria-modal="true"
          onSubmit=${(e) => { e.preventDefault(); onSubmit(e); }}>${content}</form>`
      : html`<div ref=${ref} tabindex="-1" class=${'modal' + (wide ? ' wide' : '')} role="dialog" aria-modal="true">${content}</div>`}
  </div>`;
}

// ---- confirm dialog (promise based) ---------------------------------------

const confirmStore = createStore({ current: null });

export function confirmDialog({ title, message, confirmText, tone = 'expired', icon = 'alert' }) {
  return new Promise((resolve) => {
    confirmStore.set({ current: { title, message, confirmText, tone, icon, resolve } });
  });
}

export function ConfirmHost() {
  const { current } = useStore(confirmStore);
  const [busy, setBusy] = useState(false);
  if (!current) return null;
  const close = (v) => { current.resolve(v); confirmStore.set({ current: null }); setBusy(false); };
  return html`<${Modal} title=${current.title} icon=${current.icon} tone=${current.tone} onClose=${() => close(false)}
    footer=${html`
      <${Button} onClick=${() => close(false)}>${t('common.cancel')}<//>
      <${Button} kind=${current.tone === 'expired' ? 'danger' : 'primary'} class=${current.tone === 'expired' ? 'solid' : ''}
        loading=${busy} onClick=${() => { setBusy(true); close(true); }}>${current.confirmText || t('common.confirm')}<//>`}>
    <p class="muted">${current.message}</p>
  <//>`;
}

// ---- toasts ----------------------------------------------------------------

export function Toasts() {
  const { items } = useStore(toasts);
  const icons = { ok: 'checkCircle', expired: 'alertCircle', warning: 'alert', accent: 'info' };
  return html`<div class="toasts" aria-live="polite">
    ${items.map((it) => html`<div class=${'toast tone-' + it.tone} key=${it.id}>
      <${Icon} name=${icons[it.tone] || 'info'} />
      <div class="msg">${it.title && html`<b>${it.title}</b>`}${it.message}</div>
      <button class="close" aria-label=${t('common.close')} onClick=${() => dismissToast(it.id)}><${Icon} name="x" size="14" /></button>
    </div>`)}
  </div>`;
}

export function toastError(err, title) {
  toast(errorText(err), { tone: 'expired', title: title || t('common.error'), timeout: 7000 });
}

// ---- menus -----------------------------------------------------------------

// Popover renders floating content into <body>, outside every overflow
// container and stacking context, and keeps it attached to its anchor.
// It flips above the anchor when there is no room below.
export function Popover({ anchor, onClose, align = 'end', matchWidth = false, children }) {
  const ref = useRef(null);
  const close = useRef(onClose);
  close.current = onClose;
  const [pos, setPos] = useState(null);

  useLayoutEffect(() => {
    const place = () => {
      const a = anchor.current;
      const el = ref.current;
      if (!a || !el) return;
      const r = a.getBoundingClientRect();
      const vw = document.documentElement.clientWidth;
      const vh = window.innerHeight;
      const gap = 6;
      const w = matchWidth ? r.width : el.offsetWidth;
      const h = el.offsetHeight;
      let left = align === 'end' ? r.right - w : r.left;
      left = Math.max(8, Math.min(left, vw - w - 8));
      let top = r.bottom + gap;
      if (top + h > vh - 8 && r.top - gap - h >= 8) top = r.top - gap - h;
      setPos((p) => (p && p.top === top && p.left === left && p.width === r.width ? p : { top, left, width: r.width }));
    };
    place();
    const ro = new ResizeObserver(place);
    ro.observe(ref.current);
    window.addEventListener('resize', place);
    window.addEventListener('scroll', place, true);
    return () => {
      ro.disconnect();
      window.removeEventListener('resize', place);
      window.removeEventListener('scroll', place, true);
    };
  }, [anchor, align, matchWidth]);

  useEffect(() => {
    const down = (e) => {
      if (!ref.current?.contains(e.target) && !anchor.current?.contains(e.target)) close.current();
    };
    // Capture phase so Escape closes the popover and not a surrounding modal.
    const key = (e) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        close.current();
      }
    };
    document.addEventListener('mousedown', down);
    document.addEventListener('keydown', key, true);
    return () => {
      document.removeEventListener('mousedown', down);
      document.removeEventListener('keydown', key, true);
    };
  }, [anchor]);

  return createPortal(html`<div ref=${ref} class="popover" style=${{
    top: (pos ? pos.top : -9999) + 'px', left: (pos ? pos.left : -9999) + 'px',
    width: matchWidth && pos ? pos.width + 'px' : undefined, visibility: pos ? 'visible' : 'hidden',
  }}>${children}</div>`, document.body);
}

export function Menu({ icon = 'more', label, items, size = 'sm' }) {
  const [open, setOpen] = useState(false);
  const anchor = useRef(null);
  const visible = items.filter(Boolean);
  if (!visible.length) return null;
  return html`<div class="menu-wrap" ref=${anchor} onClick=${(e) => e.stopPropagation()}>
    <${Button} kind="ghost" size=${size} icon=${icon} aria-label=${label || t('common.actions')} aria-haspopup="menu"
      aria-expanded=${open} onClick=${() => setOpen(!open)}>${label && icon !== 'more' ? label : null}<//>
    ${open && html`<${Popover} anchor=${anchor} align="end" onClose=${() => setOpen(false)}>
      <div class="menu" role="menu">
        ${visible.map((it) => it === '-' ? html`<hr />` : html`<button type="button" role="menuitem" class=${it.danger ? 'danger' : ''}
          onClick=${() => { setOpen(false); it.onClick(); }}>${it.icon && html`<${Icon} name=${it.icon} />`}${it.label}</button>`)}
      </div>
    <//>`}
  </div>`;
}

// ---- misc -------------------------------------------------------------------

export function CopyButton({ text, label }) {
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement('textarea');
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
    }
    setDone(true);
    setTimeout(() => setDone(false), 1500);
  };
  return html`<${Button} kind="ghost" size="xs" icon=${done ? 'check' : 'copy'} onClick=${copy}
    aria-label=${t('common.copy')} title=${t('common.copy')}>${label ? (done ? t('common.copied') : label) : null}<//>`;
}

export function Pager({ total, offset, limit, onChange }) {
  if (total <= limit && offset === 0) return null;
  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(offset + limit, total);
  return html`<div class="pager">
    <span>${t('common.range', { from, to, total })}</span>
    <${Button} size="sm" icon="chevronLeft" disabled=${offset === 0} aria-label=${t('common.prev')}
      onClick=${() => onChange(Math.max(0, offset - limit))} />
    <${Button} size="sm" icon="chevronRight" disabled=${to >= total} aria-label=${t('common.next')}
      onClick=${() => onChange(offset + limit)} />
  </div>`;
}

export function Callout({ tone = 'accent', icon = 'info', children, action }) {
  return html`<div class=${'callout tone-' + tone}><${Icon} name=${icon} /><div class="grow">${children}</div>${action}</div>`;
}

export function Avatar({ name, email }) {
  const src = (name || email || '?').trim();
  const parts = src.split(/[\s@._-]+/).filter(Boolean);
  const initials = ((parts[0]?.[0] || '?') + (parts[1]?.[0] || '')).toUpperCase();
  return html`<span class="avatar" aria-hidden="true">${initials}</span>`;
}
