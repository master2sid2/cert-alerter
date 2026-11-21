// Alert recipients (e-mail addresses) and their groups.
import { html, useMemo, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import { Button, Card, Empty, ErrorState, FormError, Menu, Modal, Skeleton, TextField, confirmDialog, toastError } from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { GroupSelect } from '../components/pickers.js';
import { GroupChips } from '../components/status.js';
import { del, get, post, put } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { useAsync, useTitle } from '../lib/hooks.js';
import { t, tp, fmtDate } from '../lib/i18n.js';

function RecipientModal({ recipient, groups, onClose, onSaved }) {
  const [email, setEmail] = useState(recipient?.email || '');
  const [name, setName] = useState(recipient?.name || '');
  const [groupIds, setGroupIds] = useState(recipient?.group_ids || []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const body = { email, name, group_ids: groupIds };
      const r = recipient ? await put('/recipients/' + recipient.id, body) : await post('/recipients', body);
      toast(recipient ? t('common.saved') : t('rcpt.created', { email: r.email }));
      onSaved?.(r);
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return html`<${Modal} title=${recipient ? t('rcpt.edit_title') : t('rcpt.new_title')} sub=${t('rcpt.new_sub')} icon="mail"
    onClose=${onClose} onSubmit=${submit}
    footer=${html`<${Button} onClick=${onClose}>${t('common.cancel')}<//><${Button} kind="primary" type="submit" loading=${busy} disabled=${!email.trim()}>${recipient ? t('common.save') : t('common.add')}<//>`}>
    <div class="stack">
      ${error && !error.fields && html`<${FormError} error=${error} />`}
      <${TextField} label=${t('common.email')} type="email" value=${email} onInput=${setEmail} required error=${error?.fields?.email} />
      <${TextField} label=${t('common.name')} value=${name} onInput=${setName} hint=${t('common.optional')} error=${error?.fields?.name} />
      <${GroupSelect} groups=${groups} value=${groupIds} onChange=${setGroupIds} label=${t('nav.groups')} hint=${t('rcpt.groups_hint')} />
    </div>
  <//>`;
}

export function Recipients() {
  const me = app.get().me;
  const isAdmin = me.user.role === 'admin';
  const [q, setQ] = useState('');
  const [editing, setEditing] = useState(null);
  const list = useAsync(() => get('/recipients'), []);
  const groups = useAsync(() => get('/groups'), []);
  useTitle(t('nav.recipients'), me.app.name);
  const filtered = useMemo(() => (list.data || []).filter((r) => (r.email + ' ' + r.name).toLowerCase().includes(q.toLowerCase())), [list.data, q]);
  if (list.error) return html`<div class="page"><${ErrorState} error=${list.error} onRetry=${list.reload} /></div>`;

  const remove = async (r) => {
    if (!(await confirmDialog({ title: t('rcpt.delete_title'), message: t('rcpt.delete_text', { email: r.email, n: r.group_ids.length }), confirmText: t('common.delete') }))) return;
    try {
      await del('/recipients/' + r.id);
      toast(t('rcpt.deleted'));
      list.reload(true);
    } catch (e) {
      toastError(e);
    }
  };

  return html`<div class="page stack">
    <${PageHead} title=${t('nav.recipients')} sub=${t('rcpt.sub')}
      actions=${isAdmin && html`<${Button} kind="primary" icon="plus" onClick=${() => setEditing({})}>${t('rcpt.new')}<//>`} />
    <div class="row-wrap reveal" style=${{ '--i': 1 }}>
      <div class="input-group" style=${{ flex: '1 1 260px', maxWidth: '420px' }}>
        <${Icon} name="search" />
        <input class="input" type="search" placeholder=${t('rcpt.search')} value=${q} onInput=${(e) => setQ(e.target.value)} aria-label=${t('common.search')} />
      </div>
      ${!isAdmin && html`<span class="badge outline"><${Icon} name="lock" size="12" />${t('common.read_only')}</span>`}
    </div>
    <${Card} class="reveal" style=${{ '--i': 2 }} flush>
      ${!list.data ? html`<${Skeleton} rows=${6} />`
        : list.data.length === 0 ? html`<${Empty} icon="mail" title=${t('rcpt.empty_title')}
            action=${isAdmin && html`<${Button} kind="primary" icon="plus" onClick=${() => setEditing({})}>${t('rcpt.new')}<//>`}>${t('rcpt.empty_text')}<//>`
        : filtered.length === 0 ? html`<${Empty} icon="search" title=${t('certs.no_match')} />`
        : html`<div class="table-wrap"><table class="table"><thead><tr>
            <th>${t('common.email')}</th><th>${t('common.name')}</th><th>${t('nav.groups')}</th><th>${t('rcpt.added')}</th>${isAdmin && html`<th></th>`}
          </tr></thead><tbody>${filtered.map((r) => html`<tr>
            <td><span class="row" style=${{ gap: '10px' }}><span class="avatar" style=${{ width: '28px', height: '28px', fontSize: '11px' }}>${(r.name || r.email)[0].toUpperCase()}</span><b style=${{ fontWeight: 550 }}>${r.email}</b></span></td>
            <td>${r.name || html`<span class="faint">—</span>`}</td>
            <td><${GroupChips} ids=${r.group_ids} groups=${groups.data} max=${4} /></td>
            <td class="muted nowrap">${fmtDate(r.created_at)}</td>
            ${isAdmin && html`<td class="actions"><${Menu} items=${[
              { icon: 'edit', label: t('common.edit'), onClick: () => setEditing(r) },
              '-',
              { icon: 'trash', label: t('common.delete'), danger: true, onClick: () => remove(r) },
            ]} /></td>`}
          </tr>`)}</tbody></table></div>`}
      ${list.data?.length > 0 && html`<div class="pager"><span>${tp('unit.recipients', filtered.length)}</span></div>`}
    <//>
    ${editing && html`<${RecipientModal} recipient=${editing.id ? editing : null} groups=${groups.data || []}
      onClose=${() => setEditing(null)} onSaved=${() => list.reload(true)} />`}
  </div>`;
}
