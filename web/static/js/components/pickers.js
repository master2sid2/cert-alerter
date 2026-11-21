// Composite inputs: group multi-select, certificate picker, password field.
import { html, useMemo, useRef, useState, useId } from '../../vendor/preact-htm.js';
import { Icon } from './icons.js';
import { Button, Field, Modal, Popover } from './ui.js';
import { groupColor, StatusBadge, certTitle, DaysLeft } from './status.js';
import { t, tp } from '../lib/i18n.js';

// GroupSelect lets the user pick groups; locked ids cannot be removed.
export function GroupSelect({ groups, value, onChange, label, hint, locked = [] }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState('');
  const anchor = useRef(null);
  const selected = new Set(value);
  const byId = new Map(groups.map((g) => [g.id, g]));
  const available = groups.filter((g) => !selected.has(g.id) && g.name.toLowerCase().includes(q.toLowerCase()));
  const add = (id) => { onChange([...value, id]); setQ(''); };
  const remove = (id) => onChange(value.filter((v) => v !== id));
  return html`<${Field} label=${label} hint=${hint}>
    <div>
      <div ref=${anchor} class="row-wrap input" style=${{ height: 'auto', minHeight: '38px', padding: '6px 8px', cursor: 'text' }}
        onClick=${() => setOpen(true)}>
        ${value.map((id) => html`<span class="chip group" style=${{ '--chip-color': groupColor(id) }}>
          <span>${byId.get(id)?.name || '#' + id}</span>
          ${!locked.includes(id) && html`<button type="button" class="x" aria-label=${t('common.remove')}
            onClick=${(e) => { e.stopPropagation(); remove(id); }}><${Icon} name="x" /></button>`}
        </span>`)}
        <input value=${q} onInput=${(e) => { setQ(e.target.value); setOpen(true); }} onFocus=${() => setOpen(true)}
          placeholder=${value.length ? '' : t('groups.pick_placeholder')}
          onKeyDown=${(e) => {
            if (e.key === 'Enter') { e.preventDefault(); if (available[0]) add(available[0].id); }
            if (e.key === 'Backspace' && !q && value.length && !locked.includes(value[value.length - 1])) remove(value[value.length - 1]);
            if (e.key === 'Escape') setOpen(false);
          }}
          style=${{ border: 0, outline: 0, background: 'transparent', flex: 1, minWidth: '120px', height: '24px' }} />
      </div>
      ${open && html`<${Popover} anchor=${anchor} align="start" matchWidth onClose=${() => setOpen(false)}>
      <div class="menu" style=${{ width: '100%', maxHeight: '240px', overflowY: 'auto' }}>
        ${available.length === 0
          ? html`<div class="muted" style=${{ padding: '8px 10px', fontSize: '13px' }}>${groups.length ? t('groups.pick_none') : t('groups.none_yet')}</div>`
          : available.map((g) => html`<button type="button" onClick=${() => add(g.id)}>
              <span class="chip group" style=${{ '--chip-color': groupColor(g.id), border: 0, background: 'none', padding: 0 }}><span>${g.name}</span></span>
              <span class="spacer"></span><span class="faint" style=${{ fontSize: '11.5px' }}>${tp('unit.certs', g.certificate_count || 0)}</span>
            </button>`)}
      </div>
      <//>`}
    </div>
  <//>`;
}

// CertPicker selects certificates (e.g. to add them to a group).
export function CertPicker({ certs, exclude = [], onClose, onPick, title }) {
  const [q, setQ] = useState('');
  const [sel, setSel] = useState(new Set());
  const [busy, setBusy] = useState(false);
  const ex = new Set(exclude);
  const list = useMemo(() => certs.filter((c) => !ex.has(c.id) && [c.name, c.url, c.subject_cn, c.host]
    .join(' ').toLowerCase().includes(q.toLowerCase())), [certs, q]); // eslint-disable-line react-hooks/exhaustive-deps
  const toggle = (id) => { const n = new Set(sel); n.has(id) ? n.delete(id) : n.add(id); setSel(n); };
  const allSel = list.length > 0 && list.every((c) => sel.has(c.id));
  return html`<${Modal} title=${title} wide onClose=${onClose}
    footer=${html`<span class="muted" style=${{ marginRight: 'auto' }}>${tp('unit.selected', sel.size)}</span>
      <${Button} onClick=${onClose}>${t('common.cancel')}<//>
      <${Button} kind="primary" icon="plus" disabled=${!sel.size} loading=${busy}
        onClick=${async () => { setBusy(true); try { await onPick([...sel]); } finally { setBusy(false); } }}>${t('common.add')}<//>`}>
    <div class="stack-sm">
      <div class="input-group"><${Icon} name="search" />
        <input class="input" placeholder=${t('certs.search_placeholder')} value=${q} onInput=${(e) => setQ(e.target.value)} /></div>
      <div class="card" style=${{ maxHeight: '420px', overflowY: 'auto', boxShadow: 'none' }}>
        ${list.length === 0
          ? html`<div class="empty" style=${{ padding: '28px' }}>${t('certs.picker_empty')}</div>`
          : html`<table class="table"><thead><tr>
              <th style=${{ width: '36px' }}><input type="checkbox" aria-label=${t('common.select_all')} checked=${allSel}
                onChange=${() => setSel(allSel ? new Set() : new Set(list.map((c) => c.id)))} /></th>
              <th>${t('certs.col.endpoint')}</th><th>${t('certs.col.status')}</th><th class="num">${t('certs.col.left')}</th>
            </tr></thead><tbody>
              ${list.map((c) => html`<tr class="click" onClick=${() => toggle(c.id)}>
                <td><input type="checkbox" checked=${sel.has(c.id)} onClick=${(e) => e.stopPropagation()} onChange=${() => toggle(c.id)} /></td>
                <td><div class="cell-main"><span class="title truncate">${certTitle(c)}</span><span class="endpoint truncate">${c.url}</span></div></td>
                <td><${StatusBadge} status=${c.status} enabled=${c.enabled} /></td>
                <td class="num"><${DaysLeft} cert=${c} /></td>
              </tr>`)}
            </tbody></table>`}
      </div>
    </div>
  <//>`;
}

// policyChecks evaluates a password against the policy client side (the
// server performs the authoritative check).
export function policyChecks(pw, policy, email) {
  const checks = [{ key: 'min_length', ok: [...pw].length >= (policy?.min_length || 1), n: policy?.min_length }];
  if (policy?.require_upper) checks.push({ key: 'upper', ok: /\p{Lu}/u.test(pw) });
  if (policy?.require_lower) checks.push({ key: 'lower', ok: /\p{Ll}/u.test(pw) });
  if (policy?.require_digit) checks.push({ key: 'digit', ok: /\p{Nd}/u.test(pw) });
  if (policy?.require_special) checks.push({ key: 'special', ok: /[^\p{L}\p{Nd}]/u.test(pw) });
  const local = (email || '').split('@')[0].toLowerCase();
  if (local.length >= 4) checks.push({ key: 'contains_email', ok: !pw.toLowerCase().includes(local) });
  return checks;
}

export function PasswordField({ label, value, onInput, policy, email, error, violations, autoComplete = 'new-password', showRules = true }) {
  const [show, setShow] = useState(false);
  const id = useId();
  const checks = policyChecks(value || '', policy, email);
  const passed = checks.filter((c) => c.ok).length;
  const strength = !value ? 0 : Math.min(4, Math.round((passed / checks.length) * 3) + ([...value].length >= 16 ? 1 : 0));
  const colors = ['var(--surface-3)', 'var(--exp)', 'var(--crit)', 'var(--warn)', 'var(--ok)'];
  const serverBad = new Set(violations || []);
  return html`<${Field} label=${label} error=${error} htmlFor=${id}>
    <div class="input-group">
      <${Icon} name="key" />
      <input id=${id} class="input" type=${show ? 'text' : 'password'} value=${value} autocomplete=${autoComplete}
        aria-invalid=${error ? 'true' : undefined} onInput=${(e) => onInput(e.target.value)} style=${{ paddingRight: '42px' }} />
      <span class="input-suffix"><${Button} kind="ghost" size="xs" icon=${show ? 'eyeOff' : 'eye'}
        aria-label=${show ? t('pw.hide') : t('pw.show')} onClick=${() => setShow(!show)} /></span>
    </div>
    ${showRules && html`
      <div class="pw-meter" aria-hidden="true">${[1, 2, 3, 4].map((i) => html`<span style=${{ background: i <= strength ? colors[strength] : undefined }}></span>`)}</div>
      <ul class="pw-rules">
        ${checks.map((c) => html`<li class=${c.ok ? 'ok' : serverBad.has(c.key) ? 'bad' : ''}>
          <${Icon} name=${c.ok ? 'checkCircle' : 'xCircle'} />${t('pw.rule.' + c.key, { n: c.n })}
        </li>`)}
        ${serverBad.has('reused') && html`<li class="bad"><${Icon} name="xCircle" />${t('pw.rule.reused', { n: policy?.history_count })}</li>`}
      </ul>`}
  <//>`;
}

export function policySatisfied(pw, policy, email) {
  return policyChecks(pw || '', policy, email).every((c) => c.ok);
}
