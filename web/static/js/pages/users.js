// Application users (admin only): invitations, roles, status, lockouts.
import { html, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import {
  Avatar, Button, Callout, Card, CopyButton, Empty, ErrorState, FormError, Menu, Modal, SelectField, Skeleton, TextField,
  confirmDialog, toastError,
} from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { del, get, post, put } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { useAsync, useTitle } from '../lib/hooks.js';
import { t, fmtDateTime, fmtRelative } from '../lib/i18n.js';

function LinkResult({ result, kind }) {
  return html`<div class="stack-sm">
    ${result.email_sent
      ? html`<${Callout} tone="ok" icon="mail">${t(kind === 'invite' ? 'users.invite_sent' : 'users.reset_sent')}<//>`
      : html`<${Callout} tone="warning" icon="alert">${t('users.email_failed')} <span class="mono">${result.email_error}</span><//>`}
    <div class="field"><span class="label">${t('users.link')}</span>
      <div class="row"><input class="input mono" readonly value=${result.url} onFocus=${(e) => e.target.select()} style=${{ fontSize: '12px' }} />
        <${CopyButton} text=${result.url} label=${t('common.copy')} /></div>
      <span class="hint">${t('users.link_valid', { time: fmtDateTime(result.expires_at) })}</span>
    </div>
  </div>`;
}

function InviteModal({ onClose, onDone }) {
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [role, setRole] = useState('user');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const [result, setResult] = useState(null);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await post('/users', { email, name, role });
      setResult(r.invite);
      onDone?.();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  if (result) {
    return html`<${Modal} title=${t('users.invite_created')} icon="userPlus" tone="ok" onClose=${onClose}
      footer=${html`<${Button} kind="primary" onClick=${onClose}>${t('common.done')}<//>`}>
      <${LinkResult} result=${result} kind="invite" />
    <//>`;
  }
  return html`<${Modal} title=${t('users.invite_title')} sub=${t('users.invite_sub')} icon="userPlus" onClose=${onClose} onSubmit=${submit}
    footer=${html`<${Button} onClick=${onClose}>${t('common.cancel')}<//><${Button} kind="primary" type="submit" icon="send" loading=${busy} disabled=${!email.trim()}>${t('users.send_invite')}<//>`}>
    <div class="stack">
      ${error && !error.fields && html`<${FormError} error=${error} />`}
      <${TextField} label=${t('common.email')} type="email" value=${email} onInput=${setEmail} required error=${error?.fields?.email} />
      <${TextField} label=${t('common.name')} value=${name} onInput=${setName} hint=${t('users.name_hint')} error=${error?.fields?.name} />
      <${SelectField} label=${t('users.role')} value=${role} onChange=${setRole}
        options=${[{ value: 'user', label: t('role.user') }, { value: 'admin', label: t('role.admin') }]}
        hint=${role === 'admin' ? t('users.role_admin_hint') : t('users.role_user_hint')} />
    </div>
  <//>`;
}

export function Users() {
  const me = app.get().me;
  const users = useAsync(() => get('/users'), []);
  const [inviting, setInviting] = useState(false);
  const [linkResult, setLinkResult] = useState(null);
  useTitle(t('nav.users'), me.app.name);
  if (users.error) return html`<div class="page"><${ErrorState} error=${users.error} onRetry=${users.reload} /></div>`;

  const act = async (fn, okMsg) => {
    try {
      const r = await fn();
      if (okMsg) toast(okMsg);
      users.reload(true);
      return r;
    } catch (e) {
      toastError(e);
      return null;
    }
  };
  const setRole = (u, role) => act(() => put('/users/' + u.id, { role }), t('users.role_changed'));
  const setStatus = async (u, status) => {
    if (status === 'disabled' && !(await confirmDialog({ title: t('users.disable_title'), message: t('users.disable_text', { email: u.email }), confirmText: t('users.disable'), tone: 'warning', icon: 'lock' }))) return;
    act(() => put('/users/' + u.id, { status }), status === 'active' ? t('users.enabled') : t('users.disabled'));
  };
  const remove = async (u) => {
    if (!(await confirmDialog({ title: t('users.delete_title'), message: t('users.delete_text', { email: u.email }), confirmText: t('common.delete') }))) return;
    act(() => del('/users/' + u.id), t('users.deleted'));
  };
  const resend = async (u) => {
    const r = await act(() => post(`/users/${u.id}/invite`));
    if (r) setLinkResult({ kind: 'invite', ...r.invite });
  };
  const reset = async (u) => {
    if (!(await confirmDialog({ title: t('users.reset_title'), message: t('users.reset_text', { email: u.email }), confirmText: t('users.send_reset'), tone: 'accent', icon: 'key' }))) return;
    const r = await act(() => post(`/users/${u.id}/reset`));
    if (r) setLinkResult({ kind: 'reset', ...r.reset });
  };
  const unlock = (u) => act(() => post(`/users/${u.id}/unlock`), t('users.unlocked'));

  const statusBadge = (u) => {
    if (u.locked_until) return html`<span class="badge tone-expired" title=${fmtDateTime(u.locked_until)}><${Icon} name="lock" size="11" />${t('users.status_locked')}</span>`;
    const tone = { active: 'ok', invited: 'warning', disabled: 'pending' }[u.status];
    return html`<span class=${'badge tone-' + tone}><span class="dot"></span>${t('users.status_' + u.status)}</span>`;
  };

  return html`<div class="page stack">
    <${PageHead} title=${t('nav.users')} sub=${t('users.sub')}
      actions=${html`<${Button} kind="primary" icon="userPlus" onClick=${() => setInviting(true)}>${t('users.invite')}<//>`} />
    <${Card} class="reveal" style=${{ '--i': 1 }} flush>
      ${!users.data ? html`<${Skeleton} rows=${5} />`
        : users.data.length === 0 ? html`<${Empty} icon="users" title=${t('users.empty')} />`
        : html`<div class="table-wrap"><table class="table"><thead><tr>
            <th>${t('users.user')}</th><th>${t('users.role')}</th><th>${t('common.status')}</th>
            <th>${t('users.last_login')}</th><th class="num">${t('users.sessions')}</th><th></th>
          </tr></thead><tbody>${users.data.map((u) => {
            const self = u.id === me.user.id;
            return html`<tr>
              <td><div class="row"><${Avatar} name=${u.name} email=${u.email} />
                <div class="cell-main"><span class="title">${u.name || u.email}${self && html` <span class="badge outline" style=${{ marginLeft: '4px' }}>${t('users.you')}</span>`}</span>
                  <span class="subtitle">${u.email}</span></div></div></td>
              <td><span class=${'badge square ' + (u.role === 'admin' ? 'tone-accent' : 'outline')}>
                <${Icon} name=${u.role === 'admin' ? 'shieldCheck' : 'user'} size="11" />${t('role.' + u.role)}</span></td>
              <td><div class="cell-main">${statusBadge(u)}
                ${u.status === 'invited' && html`<span class="subtitle">${u.invite_expires_at ? t('users.invite_expires', { time: fmtRelative(u.invite_expires_at) }) : t('users.invite_expired')}</span>`}</div></td>
              <td class="nowrap muted">${u.last_login_at ? fmtRelative(u.last_login_at) : t('common.never')}</td>
              <td class="num mono">${u.sessions || 0}</td>
              <td class="actions">${!self && html`<${Menu} items=${[
                u.role === 'user' ? { icon: 'shieldCheck', label: t('users.make_admin'), onClick: () => setRole(u, 'admin') }
                  : { icon: 'user', label: t('users.make_user'), onClick: () => setRole(u, 'user') },
                u.status === 'invited' && { icon: 'send', label: t('users.resend_invite'), onClick: () => resend(u) },
                u.status === 'active' && { icon: 'key', label: t('users.send_reset'), onClick: () => reset(u) },
                u.locked_until && { icon: 'unlock', label: t('users.unlock'), onClick: () => unlock(u) },
                u.status === 'active' && { icon: 'lock', label: t('users.disable'), onClick: () => setStatus(u, 'disabled') },
                u.status === 'disabled' && { icon: 'unlock', label: t('users.enable'), onClick: () => setStatus(u, 'active') },
                '-',
                { icon: 'trash', label: t('common.delete'), danger: true, onClick: () => remove(u) },
              ]} />`}</td>
            </tr>`;
          })}</tbody></table></div>`}
    <//>
    <${Callout} tone="accent" icon="info">${t('users.roles_note')}<//>
    ${inviting && html`<${InviteModal} onClose=${() => setInviting(false)} onDone=${() => users.reload(true)} />`}
    ${linkResult && html`<${Modal} title=${linkResult.kind === 'invite' ? t('users.invite_created') : t('users.reset_created')}
      icon=${linkResult.kind === 'invite' ? 'userPlus' : 'key'} onClose=${() => setLinkResult(null)}
      footer=${html`<${Button} kind="primary" onClick=${() => setLinkResult(null)}>${t('common.done')}<//>`}>
      <${LinkResult} result=${linkResult} kind=${linkResult.kind} />
    <//>`}
  </div>`;
}
