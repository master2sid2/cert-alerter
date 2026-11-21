// Audit log, profile and not-found pages.
import { html, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import { Avatar, Button, Card, Empty, ErrorState, FormError, Pager, Skeleton, TextField, toastError } from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { PasswordField, policySatisfied } from '../components/pickers.js';
import { get, post, put } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { Link, setQuery, useLocation } from '../lib/router.js';
import { useAsync, useTitle } from '../lib/hooks.js';
import { t, has, fmtDateTime, fmtRelative } from '../lib/i18n.js';

const LIMIT = 50;

function details(json) {
  if (!json) return null;
  try {
    const obj = JSON.parse(json);
    return Object.entries(obj).map(([k, v]) => html`<span class="chip" style=${{ height: '20px', fontSize: '11px', marginRight: '4px' }}>
      <span><b>${k}</b>: ${Array.isArray(v) ? v.join(', ') : typeof v === 'object' && v !== null ? JSON.stringify(v) : String(v)}</span></span>`);
  } catch {
    return json;
  }
}

export function Audit() {
  const me = app.get().me;
  const { query } = useLocation();
  const offset = Number(query.get('offset') || 0);
  const [q, setQ] = useState(query.get('q') || '');
  useTitle(t('nav.audit'), me.app.name);
  const params = new URLSearchParams({ limit: LIMIT, offset });
  if (query.get('q')) params.set('q', query.get('q'));
  const data = useAsync(() => get('/audit?' + params), [params.toString()]);
  if (data.error) return html`<div class="page"><${ErrorState} error=${data.error} onRetry=${data.reload} /></div>`;
  return html`<div class="page stack">
    <${PageHead} title=${t('nav.audit')} sub=${t('audit.sub')} />
    <form class="input-group reveal" style=${{ maxWidth: '420px', '--i': 1 }} onSubmit=${(e) => { e.preventDefault(); setQuery({ q, offset: '' }); }}>
      <${Icon} name="search" />
      <input class="input" type="search" placeholder=${t('audit.search')} value=${q} onInput=${(e) => setQ(e.target.value)}
        onBlur=${() => setQuery({ q, offset: '' })} aria-label=${t('common.search')} />
    </form>
    <${Card} class="reveal" style=${{ '--i': 2 }} flush>
      ${!data.data ? html`<${Skeleton} rows=${8} />`
        : data.data.items.length === 0 ? html`<${Empty} icon="list" title=${t('audit.empty')} />`
        : html`<div class="table-wrap"><table class="table"><thead><tr>
            <th>${t('notif.col.time')}</th><th>${t('audit.user')}</th><th>${t('audit.action')}</th><th>${t('audit.details')}</th><th>IP</th>
          </tr></thead><tbody>${data.data.items.map((e) => html`<tr>
            <td class="nowrap"><div class="cell-main"><span>${fmtDateTime(e.created_at)}</span><span class="subtitle">${fmtRelative(e.created_at)}</span></div></td>
            <td>${e.user_email || html`<span class="faint">—</span>`}</td>
            <td><span class=${'badge square ' + (e.action.includes('failed') || e.action.includes('locked') ? 'tone-expired' : e.action.includes('deleted') ? 'tone-warning' : 'outline')}>
              ${has('audit.a.' + e.action) ? t('audit.a.' + e.action) : e.action}</span></td>
            <td style=${{ maxWidth: '420px' }}>${details(e.details)}</td>
            <td class="mono muted" style=${{ fontSize: '12px' }}>${e.ip}</td>
          </tr>`)}</tbody></table></div>`}
      ${data.data && html`<${Pager} total=${data.data.total} offset=${offset} limit=${LIMIT} onChange=${(o) => setQuery({ offset: o || '' })} />`}
    <//>
  </div>`;
}

export function Profile() {
  const state = app.get();
  const me = state.me;
  const [name, setName] = useState(me.user.name);
  const [savingName, setSavingName] = useState(false);
  const [cur, setCur] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  useTitle(t('nav.profile'), me.app.name);
  const policy = me.app.password_policy;

  const saveName = async (e) => {
    e.preventDefault();
    setSavingName(true);
    try {
      app.set({ me: await put('/me', { name }) });
      toast(t('common.saved'));
    } catch (err) {
      toastError(err);
    } finally {
      setSavingName(false);
    }
  };
  const changePassword = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      app.set({ me: await post('/me/password', { current_password: cur, new_password: pw }) });
      toast(t('profile.password_changed'));
      setCur(''); setPw(''); setPw2('');
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return html`<div class="page stack" style=${{ maxWidth: '860px' }}>
    <${PageHead} title=${t('nav.profile')} />
    <${Card} class="reveal" style=${{ '--i': 1 }}>
      <div class="row" style=${{ gap: '16px' }}>
        <span style=${{ transform: 'scale(1.5)', margin: '8px' }}><${Avatar} name=${me.user.name} email=${me.user.email} /></span>
        <div class="grow"><h2 style=${{ fontSize: '18px' }}>${me.user.name || me.user.email}</h2>
          <div class="muted">${me.user.email} · ${t('role.' + me.user.role)}</div></div>
        <div class="muted" style=${{ fontSize: '12.5px', textAlign: 'right' }}>
          ${t('profile.password_changed_at')}<br /><b>${fmtDateTime(me.user.password_changed_at)}</b></div>
      </div>
    <//>
    <div class="grid grid-2">
      <form onSubmit=${saveName}><${Card} class="reveal" style=${{ '--i': 2 }} title=${t('profile.details')}>
        <div class="stack">
          <${TextField} label=${t('common.name')} value=${name} onInput=${setName} autocomplete="name" />
          <${TextField} label=${t('common.email')} value=${me.user.email} disabled hint=${t('profile.email_hint')} />
          <div class="form-actions"><${Button} kind="primary" type="submit" loading=${savingName}>${t('common.save')}<//></div>
        </div>
      <//></form>
      <form onSubmit=${changePassword}><${Card} class="reveal" style=${{ '--i': 3 }} title=${t('profile.change_password')}>
        <div class="stack">
          ${error && !['weak_password', 'validation'].includes(error.code) && html`<${FormError} error=${error} />`}
          <${TextField} label=${t('profile.current_password')} type="password" value=${cur} onInput=${setCur}
            autocomplete="current-password" error=${error?.fields?.current_password} />
          <${PasswordField} label=${t('auth.new_password')} value=${pw} onInput=${setPw} policy=${policy} email=${me.user.email}
            violations=${error?.details?.violations} />
          <${TextField} label=${t('auth.repeat_password')} type="password" value=${pw2} onInput=${setPw2} autocomplete="new-password"
            error=${pw2 && pw !== pw2 ? 'mismatch' : ''} />
          <div class="form-actions"><${Button} kind="primary" type="submit" loading=${busy}
            disabled=${!cur || !policySatisfied(pw, policy, me.user.email) || pw !== pw2}>${t('profile.change_password')}<//></div>
          <p class="muted" style=${{ fontSize: '12px' }}>${t('profile.sessions_note')}</p>
        </div>
      <//></form>
    </div>
  </div>`;
}

export function NotFound({ forbidden }) {
  return html`<div class="page"><div class="card"><${Empty} icon=${forbidden ? 'lock' : 'search'}
    title=${forbidden ? t('error.forbidden') : t('notfound.title')}
    action=${html`<${Link} href="/" class="btn">${t('notfound.home')}<//>`}>${forbidden ? t('notfound.forbidden_text') : t('notfound.text')}<//></div></div>`;
}
