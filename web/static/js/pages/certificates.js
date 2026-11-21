// Certificate list, add (single / bulk) and edit dialogs.
import { html, useMemo, useRef, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import {
  Button, Card, Empty, ErrorState, FormError, Modal, Segmented, Skeleton, Tabs, TextField, Switch, toastError,
} from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { GroupSelect } from '../components/pickers.js';
import { DaysLeft, GroupChips, LifetimeRing, StatusBadge, certTitle } from '../components/status.js';
import { get, post, put } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { navigate, setQuery, useLocation } from '../lib/router.js';
import { useAsync, useInterval, useKey, useTitle } from '../lib/hooks.js';
import { t, tp, fmtDate, fmtDateISO, fmtDateTime, fmtRelative, fieldError } from '../lib/i18n.js';

const FILTERS = ['all', 'expiring', 'critical', 'expired', 'problems', 'ok', 'pending', 'disabled'];

export function matchesFilter(c, f) {
  switch (f) {
    case 'all': return true;
    case 'expiring': return c.enabled && (c.status === 'warning' || c.status === 'critical');
    case 'problems': return c.check_failed || (c.not_after && !c.trusted);
    case 'disabled': return !c.enabled;
    case 'pending': return c.enabled && (c.status === 'pending' || c.status === 'error');
    default: return c.enabled && c.status === f;
  }
}

// Status order used for sorting: the most urgent first.
const STATUS_RANK = { expired: 0, critical: 1, warning: 2, error: 3, pending: 4, ok: 5, disabled: 6 };

const expiryTime = (c) => (c.not_after ? new Date(c.not_after).getTime() : Infinity);
const byExpiry = (a, b) => expiryTime(a) - expiryTime(b) || certTitle(a).localeCompare(certTitle(b));

// isMissing reports whether a certificate has no value in the sorted column.
// Such rows always go last, whatever the direction.
function isMissing(c, sort) {
  switch (sort) {
    case 'issuer': return !c.subject_cn;
    case 'groups': return c.group_ids.length === 0;
    case 'checked': return !c.last_checked_at;
    case 'expiry':
    case 'left': return !c.not_after;
    default: return false;
  }
}

// compare orders certificates by the given column. Ties are broken by expiry
// date so that the order is stable and meaningful in both directions.
function compare(a, b, sort, groupName) {
  switch (sort) {
    case 'name': return certTitle(a).localeCompare(certTitle(b));
    case 'status': {
      const rank = (c) => STATUS_RANK[c.enabled ? c.status : 'disabled'] ?? 4;
      return rank(a) - rank(b) || byExpiry(a, b);
    }
    case 'issuer': return (a.subject_cn || '').localeCompare(b.subject_cn || '') || byExpiry(a, b);
    case 'left': {
      const left = (c) => (c.days_left === null || c.days_left === undefined ? Infinity : c.days_left);
      return left(a) - left(b) || byExpiry(a, b);
    }
    case 'groups': {
      // Compare the sorted group names one by one, so the alphabetically
      // first group decides and a certificate in fewer groups comes first.
      const key = (c) => c.group_ids.map(groupName).sort((x, y) => x.localeCompare(y));
      const ka = key(a);
      const kb = key(b);
      for (let i = 0; i < Math.min(ka.length, kb.length); i++) {
        const d = ka[i].localeCompare(kb[i]);
        if (d) return d;
      }
      return ka.length - kb.length || byExpiry(a, b);
    }
    case 'checked': return (new Date(b.last_checked_at || 0)) - (new Date(a.last_checked_at || 0));
    default: return byExpiry(a, b);
  }
}

export function Certificates() {
  const me = app.get().me;
  const { query } = useLocation();
  const filter = FILTERS.includes(query.get('status')) ? query.get('status') : 'all';
  const group = query.get('group') || '';
  const sort = query.get('sort') || 'expiry';
  const dir = query.get('dir') === 'desc' ? -1 : 1;
  const [q, setQ] = useState(query.get('q') || '');
  const [adding, setAdding] = useState(false);
  const [checking, setChecking] = useState(false);
  const searchRef = useRef(null);
  useTitle(t('nav.certificates'), me.app.name);
  useKey('/', () => searchRef.current?.focus());

  const certs = useAsync(() => get('/certificates'), []);
  const groups = useAsync(() => get('/groups'), []);
  const anyPending = certs.data?.some((c) => c.enabled && !c.last_checked_at);
  useInterval(() => certs.reload(true), anyPending || checking ? 3000 : 60000);

  const list = useMemo(() => {
    if (!certs.data) return [];
    const names = new Map((groups.data || []).map((g) => [g.id, g.name]));
    const groupName = (id) => names.get(id) || '#' + id;
    const needle = q.trim().toLowerCase();
    return certs.data
      .filter((c) => matchesFilter(c, filter))
      .filter((c) => !group || (group === 'none' ? c.group_ids.length === 0 : c.group_ids.includes(Number(group))))
      .filter((c) => !needle || [c.name, c.url, c.host, c.subject_cn, c.issuer_cn, c.issuer_org, ...(c.sans || [])]
        .join(' ').toLowerCase().includes(needle))
      .sort((a, b) => {
        const ma = isMissing(a, sort);
        if (ma !== isMissing(b, sort)) return ma ? 1 : -1;
        return compare(a, b, sort, groupName) * dir;
      });
  }, [certs.data, groups.data, filter, group, q, sort, dir]);

  const counts = useMemo(() => Object.fromEntries(FILTERS.map((f) => [f, (certs.data || []).filter((c) => matchesFilter(c, f)).length])), [certs.data]);

  const checkAll = async () => {
    setChecking(true);
    try {
      const r = await post('/certificates/check');
      toast(r.started ? t('certs.check_started') : t('certs.check_running'), { tone: 'accent' });
    } catch (e) {
      toastError(e);
    } finally {
      setTimeout(() => { setChecking(false); certs.reload(true); }, 4000);
    }
  };

  const sortHead = (key, label, cls = '') => html`<th class=${'sortable ' + cls}
    onClick=${() => setQuery({ sort: key, dir: sort === key && dir === 1 ? 'desc' : '' })}
    aria-sort=${sort === key ? (dir === 1 ? 'ascending' : 'descending') : 'none'}>
    ${label}<span class="arrow">${sort === key ? (dir === 1 ? '↑' : '↓') : ''}</span></th>`;

  if (certs.error) return html`<div class="page"><${ErrorState} error=${certs.error} onRetry=${certs.reload} /></div>`;

  return html`<div class="page stack">
    <${PageHead} title=${t('nav.certificates')} sub=${t('certs.sub')}
      actions=${html`
        <${Button} icon="refresh" loading=${checking} onClick=${checkAll}>${t('certs.check_all')}<//>
        <${Button} kind="primary" icon="plus" onClick=${() => setAdding(true)}>${t('certs.add')}<//>`} />

    <div class="row-wrap reveal" style=${{ '--i': 1, gap: '10px' }}>
      <div class="input-group" style=${{ flex: '1 1 260px', maxWidth: '420px' }}>
        <${Icon} name="search" />
        <input ref=${searchRef} class="input" type="search" placeholder=${t('certs.search_placeholder')} value=${q}
          onInput=${(e) => { setQ(e.target.value); setQuery({ q: e.target.value }); }} aria-label=${t('common.search')} />
        <span class="kbd">/</span>
      </div>
      <select class="select" style=${{ width: 'auto', minWidth: '180px' }} value=${group} aria-label=${t('nav.groups')}
        onChange=${(e) => setQuery({ group: e.target.value })}>
        <option value="">${t('certs.all_groups')}</option>
        <option value="none">${t('certs.no_group')}</option>
        ${(groups.data || []).map((g) => html`<option value=${g.id}>${g.name}</option>`)}
      </select>
    </div>
    <div class="reveal hscroll" style=${{ '--i': 2 }}>
      <${Segmented} value=${filter} onChange=${(v) => setQuery({ status: v === 'all' ? '' : v })} label=${t('certs.filter')}
        options=${FILTERS.map((f) => ({ value: f, label: t('certs.filter_' + f), count: counts[f] }))} />
    </div>

    <${Card} class="reveal" style=${{ '--i': 3 }} flush>
      ${!certs.data ? html`<${Skeleton} rows=${8} />`
        : certs.data.length === 0 ? html`<${Empty} icon="shieldCheck" title=${t('certs.empty_title')}
            action=${html`<${Button} kind="primary" icon="plus" onClick=${() => setAdding(true)}>${t('certs.add_first')}<//>`}>${t('certs.empty_text')}<//>`
        : list.length === 0 ? html`<${Empty} icon="search" title=${t('certs.no_match')}>${t('certs.no_match_text')}<//>`
        : html`<div class="table-wrap"><table class="table">
          <thead><tr>
            ${sortHead('name', t('certs.col.endpoint'))}
            ${sortHead('status', t('certs.col.status'))}
            ${sortHead('issuer', t('certs.col.certificate'))}
            ${sortHead('expiry', t('certs.col.expires'))}
            ${sortHead('left', t('certs.col.left'), 'num')}
            ${sortHead('groups', t('certs.col.groups'))}
            ${sortHead('checked', t('certs.col.checked'))}
          </tr></thead>
          <tbody>${list.map((c) => html`<tr class="click" key=${c.id} onClick=${() => navigate('/certificates/' + c.id)}>
            <td style=${{ maxWidth: '340px' }}><div class="row">
              <${LifetimeRing} cert=${c} />
              <div class="cell-main"><a class="title truncate" href=${'/certificates/' + c.id}
                onClick=${(e) => { e.preventDefault(); }}>${certTitle(c)}</a>
                <span class="endpoint truncate">${c.url}</span></div>
            </div></td>
            <td><${StatusBadge} status=${c.status} enabled=${c.enabled} /></td>
            <td style=${{ maxWidth: '260px' }}><div class="cell-main">
              <span class="truncate">${c.subject_cn || html`<span class="faint">—</span>`}</span>
              <span class="subtitle truncate">${c.issuer_org || c.issuer_cn}</span></div></td>
            <td class="nowrap mono" style=${{ fontSize: '12.5px' }} title=${fmtDate(c.not_after)}>${fmtDateISO(c.not_after)}</td>
            <td class="num"><${DaysLeft} cert=${c} /></td>
            <td><${GroupChips} ids=${c.group_ids} groups=${groups.data} max=${2} /></td>
            <td class="nowrap">
              <span class="row" style=${{ gap: '6px' }}>
                ${c.check_failed && html`<span title=${c.last_error} style=${{ color: 'var(--err)', display: 'inline-flex' }}><${Icon} name="wifiOff" size="14" /></span>`}
                ${c.not_after && !c.trusted && html`<span title=${c.verify_error} style=${{ color: 'var(--exp)', display: 'inline-flex' }}><${Icon} name="shieldAlert" size="14" /></span>`}
                <span class="muted" style=${{ fontSize: '12.5px' }} title=${c.last_checked_at ? fmtDateTime(c.last_checked_at) : ''}>${c.last_checked_at ? fmtRelative(c.last_checked_at, true) : t('status.pending')}</span>
              </span>
            </td>
          </tr>`)}</tbody>
        </table></div>`}
      ${certs.data?.length > 0 && html`<div class="pager"><span>${t('certs.shown', { shown: list.length, total: certs.data.length })}</span></div>`}
    <//>

    ${adding && html`<${AddCertificateModal} onClose=${() => setAdding(false)} onDone=${() => certs.reload(true)}
      defaultGroups=${group && group !== 'none' ? [Number(group)] : []} />`}
  </div>`;
}

export function AddCertificateModal({ onClose, onDone, defaultGroups = [] }) {
  const [mode, setMode] = useState('single');
  const [url, setUrl] = useState('');
  const [name, setName] = useState('');
  const [notes, setNotes] = useState('');
  const [text, setText] = useState('');
  const [groupIds, setGroupIds] = useState(defaultGroups);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const [bulkResult, setBulkResult] = useState(null);
  const groups = useAsync(() => get('/groups'), []);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      if (mode === 'single') {
        const c = await post('/certificates', { url, name, notes, group_ids: groupIds });
        toast(c.check_failed ? t('certs.added_with_error', { error: c.last_error }) : t('certs.added', { name: certTitle(c) }),
          { tone: c.check_failed ? 'warning' : 'ok' });
        onDone?.();
        onClose();
        navigate('/certificates/' + c.id);
      } else {
        const r = await post('/certificates/bulk', { text, group_ids: groupIds });
        setBulkResult(r);
        onDone?.();
      }
    } catch (e) {
      if (e.code === 'duplicate' && e.details?.id) {
        setError({ ...e, duplicateId: e.details.id });
      } else setError(e);
    } finally {
      setBusy(false);
    }
  };

  if (bulkResult) {
    return html`<${Modal} title=${t('certs.bulk_result')} icon="checkCircle" tone="ok" onClose=${onClose}
      footer=${html`<${Button} kind="primary" onClick=${onClose}>${t('common.done')}<//>`}>
      <div class="stack-sm">
        <div class="row"><span class="badge tone-ok">${bulkResult.created.length}</span>${t('certs.bulk_created')}</div>
        <div class="row"><span class="badge tone-accent">${bulkResult.existing.length}</span>${t('certs.bulk_existing')}</div>
        ${bulkResult.invalid.length > 0 && html`<div class="row"><span class="badge tone-expired">${bulkResult.invalid.length}</span>${t('certs.bulk_invalid')}</div>
          <div class="card" style=${{ boxShadow: 'none', maxHeight: '180px', overflowY: 'auto' }}><div class="list">
            ${bulkResult.invalid.map((i) => html`<div class="list-item"><code class="grow truncate">${i.input}</code><span class="meta">${fieldError(i.error)}</span></div>`)}
          </div></div>`}
        <p class="muted" style=${{ fontSize: '12.5px' }}>${t('certs.bulk_checking')}</p>
      </div>
    <//>`;
  }

  const urlError = error?.fields?.url || (error?.code === 'duplicate' ? 'duplicate_endpoint' : '');
  return html`<${Modal} title=${t('certs.add_title')} sub=${t('certs.add_sub')} icon="shieldCheck" wide onClose=${onClose} onSubmit=${submit}
    footer=${html`<${Button} onClick=${onClose}>${t('common.cancel')}<//>
      <${Button} kind="primary" type="submit" icon=${mode === 'single' ? 'zap' : 'upload'} loading=${busy}
        disabled=${mode === 'single' ? !url.trim() : !text.trim()}>${mode === 'single' ? t('certs.add_and_check') : t('certs.import')}<//>`}>
    <${Tabs} value=${mode} onChange=${(m) => { setMode(m); setError(null); }}
      tabs=${[{ value: 'single', label: t('certs.mode_single'), icon: 'link' }, { value: 'bulk', label: t('certs.mode_bulk'), icon: 'list' }]} />
    <div class="stack">
      ${error && !error.fields?.url && error.code !== 'duplicate' && html`<${FormError} error=${error} />`}
      ${mode === 'single' ? html`
        <${TextField} label=${t('certs.field_url')} value=${url} onInput=${setUrl} inputClass="mono" required
          placeholder="https://example.com" hint=${t('certs.field_url_hint')} error=${urlError} autocomplete="off" />
        ${error?.duplicateId && html`<div style=${{ marginTop: '-8px' }}><a href=${'/certificates/' + error.duplicateId}
          onClick=${(e) => { e.preventDefault(); onClose(); navigate('/certificates/' + error.duplicateId); }}>${t('certs.open_existing')} →</a></div>`}
        <div class="form-grid">
          <${TextField} label=${t('certs.field_name')} value=${name} onInput=${setName} placeholder=${t('certs.field_name_ph')}
            hint=${t('common.optional')} error=${error?.fields?.name} />
          <${TextField} label=${t('certs.field_notes')} value=${notes} onInput=${setNotes} placeholder=${t('certs.field_notes_ph')}
            hint=${t('common.optional')} error=${error?.fields?.notes} />
        </div>`
      : html`<${TextField} textarea label=${t('certs.field_bulk')} value=${text} onInput=${setText} inputClass="mono"
          rows="8" placeholder=${'example.com\nhttps://api.example.com\nmail.example.com:465\n# ' + t('certs.bulk_comment')}
          hint=${t('certs.field_bulk_hint')} error=${error?.fields?.text} />`}
      <${GroupSelect} groups=${groups.data || []} value=${groupIds} onChange=${setGroupIds}
        label=${t('certs.field_groups')} hint=${t('certs.field_groups_hint')} />
    </div>
  <//>`;
}

export function EditCertificateModal({ cert, onClose, onSaved }) {
  const [url, setUrl] = useState(cert.url);
  const [name, setName] = useState(cert.name);
  const [notes, setNotes] = useState(cert.notes);
  const [enabled, setEnabled] = useState(cert.enabled);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const c = await put('/certificates/' + cert.id, { url, name, notes, enabled });
      toast(t('common.saved'));
      onSaved?.(c);
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return html`<${Modal} title=${t('cert.edit_title')} icon="edit" onClose=${onClose} onSubmit=${submit}
    footer=${html`<${Button} onClick=${onClose}>${t('common.cancel')}<//><${Button} kind="primary" type="submit" loading=${busy}>${t('common.save')}<//>`}>
    <div class="stack">
      ${error && !error.fields?.url && html`<${FormError} error=${error} />`}
      <${TextField} label=${t('certs.field_url')} value=${url} onInput=${setUrl} inputClass="mono" required
        hint=${t('cert.edit_url_hint')} error=${error?.fields?.url || (error?.code === 'duplicate' ? 'duplicate_endpoint' : '')} />
      <${TextField} label=${t('certs.field_name')} value=${name} onInput=${setName} error=${error?.fields?.name} />
      <${TextField} textarea label=${t('certs.field_notes')} value=${notes} onInput=${setNotes} rows="3" error=${error?.fields?.notes} />
      <${Switch} checked=${enabled} onChange=${setEnabled} label=${t('cert.monitoring_enabled')} hint=${t('cert.monitoring_enabled_hint')} />
    </div>
  <//>`;
}

export { tp };
