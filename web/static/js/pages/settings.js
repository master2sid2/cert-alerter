// Admin settings: general, e-mail (SMTP), monitoring, security policy.
import { html, useEffect, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import {
  Button, Callout, Card, Check, ErrorState, FormError, NumberField, PageLoader, Segmented, SelectField, Switch, Tabs,
  TextField, toastError,
} from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { get, post, put } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { navigate } from '../lib/router.js';
import { useAsync, useTitle } from '../lib/hooks.js';
import { t, tp } from '../lib/i18n.js';

const TABS = ['general', 'email', 'monitoring', 'security'];
// Suggestions for the time zone field: every IANA zone the browser knows.
const ZONES = (() => {
  try { return ['UTC', ...Intl.supportedValuesOf('timeZone').filter((z) => z !== 'UTC')]; } catch { return ['UTC']; }
})();

async function refreshMe() {
  try { app.set({ me: await get('/me') }); } catch { /* ignore */ }
}

function useSection(initial) {
  const [form, setForm] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  useEffect(() => setForm(initial), [initial]);
  const set = (k) => (v) => setForm((f) => ({ ...f, [k]: v }));
  const save = async (path, body, onSaved) => {
    setBusy(true);
    setError(null);
    try {
      const r = await put(path, body ?? form);
      toast(t('common.saved'));
      onSaved?.(r);
      refreshMe();
    } catch (e) {
      setError(e);
      if (!e.fields || !Object.keys(e.fields).length) toastError(e);
    } finally {
      setBusy(false);
    }
  };
  return { form, setForm, set, busy, error, save };
}

function SaveBar({ busy, children }) {
  return html`<div class="card-foot" style=${{ justifyContent: 'flex-end', margin: '18px -18px -18px' }}>
    ${children}<${Button} kind="primary" type="submit" icon="check" loading=${busy}>${t('common.save')}<//>
  </div>`;
}

function General({ data, onSaved }) {
  const s = useSection(data.general);
  const f = s.form;
  const fe = s.error?.fields || {};
  return html`<form onSubmit=${(e) => { e.preventDefault(); s.save('/settings/general', null, onSaved); }}>
    <${Card} title=${t('settings.general')} sub=${t('settings.general_sub')}>
      <div class="form-grid">
        <${TextField} label=${t('settings.app_name')} value=${f.app_name} onInput=${s.set('app_name')} error=${fe.app_name} />
        <${TextField} label=${t('settings.base_url')} value=${f.base_url} onInput=${s.set('base_url')} inputClass="mono"
          placeholder="https://certs.example.com" disabled=${data.base_url_locked}
          hint=${data.base_url_locked ? t('settings.base_url_locked') : t('settings.base_url_hint')} error=${fe.base_url} />
        <${SelectField} label=${t('settings.email_language')} value=${f.email_language} onChange=${s.set('email_language')}
          options=${[{ value: 'en', label: 'English' }]} hint=${t('settings.email_language_hint')} />
        <div class="field">
          <label for="tz">${t('settings.timezone')}</label>
          <input id="tz" class="input" list="tz-list" value=${f.timezone} onInput=${(e) => s.set('timezone')(e.target.value)}
            aria-invalid=${fe.timezone ? 'true' : undefined} />
          <datalist id="tz-list">${ZONES.map((z) => html`<option value=${z} />`)}</datalist>
          ${fe.timezone ? html`<div class="error">${t('field.invalid_timezone')}</div>` : html`<div class="hint">${t('settings.timezone_hint')}</div>`}
        </div>
      </div>
      <${SaveBar} busy=${s.busy} />
    <//>
  </form>`;
}

function Email({ data, onSaved }) {
  const s = useSection(data.smtp);
  const f = s.form;
  const fe = s.error?.fields || {};
  const [password, setPassword] = useState(null); // null = unchanged
  const [testTo, setTestTo] = useState(app.get().me.user.email);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState(null);
  const defaultPorts = { starttls: 587, tls: 465, none: 25 };
  const setSecurity = (v) => s.setForm((x) => ({ ...x, security: v, port: [587, 465, 25].includes(Number(x.port)) || !x.port ? defaultPorts[v] : x.port }));
  const body = () => ({ ...f, port: Number(f.port) || 0, timeout_seconds: Number(f.timeout_seconds) || 0, password: password === null ? undefined : password });
  const test = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      setTestResult(await post('/settings/smtp/test', { to: testTo, smtp: body() }));
    } catch (e) {
      setTestResult({ ok: false, error: e.fields?.to ? t('field.invalid_email') : e.message, fields: e.fields });
    } finally {
      setTesting(false);
    }
  };
  return html`<form onSubmit=${(e) => { e.preventDefault(); s.save('/settings/smtp', body(), (r) => { setPassword(null); onSaved(r); }); }}>
    <div class="stack">
      <${Card} title=${t('settings.smtp')} sub=${t('settings.smtp_sub')}>
        <div class="stack">
          <div class="field"><span class="label">${t('settings.security')}</span>
            <${Segmented} value=${f.security} onChange=${setSecurity} label=${t('settings.security')} options=${[
              { value: 'starttls', label: 'STARTTLS', icon: 'lock' },
              { value: 'tls', label: 'SSL/TLS', icon: 'shieldCheck' },
              { value: 'none', label: t('settings.security_none'), icon: 'unlock' },
            ]} />
            <span class="hint">${t('settings.security_hint_' + f.security)}</span>
          </div>
          <div class="form-grid">
            <${TextField} label=${t('settings.host')} value=${f.host} onInput=${s.set('host')} placeholder="smtp.example.com" inputClass="mono" error=${fe.host} />
            <${NumberField} label=${t('settings.port')} value=${f.port} onInput=${s.set('port')} error=${fe.port} />
            <${SelectField} label=${t('settings.auth')} value=${f.auth_method} onChange=${s.set('auth_method')} error=${fe.auth_method}
              options=${['auto', 'plain', 'login', 'cram-md5', 'none'].map((v) => ({ value: v, label: t('settings.auth_' + v.replace('-', '')) }))} />
            <${TextField} label=${t('settings.username')} value=${f.username} onInput=${s.set('username')} autocomplete="off" error=${fe.username} />
            <div class="field full">
              <label for="smtp-pw">${t('settings.password')}</label>
              ${password === null && data.smtp.has_password
                ? html`<div class="row"><input id="smtp-pw" class="input" value="••••••••••••" disabled />
                    <${Button} onClick=${() => setPassword('')}>${t('settings.change_password')}<//></div>`
                : html`<input id="smtp-pw" class="input" type="password" autocomplete="new-password" value=${password || ''}
                    onInput=${(e) => setPassword(e.target.value)} placeholder=${data.smtp.has_password ? t('settings.password_new_ph') : ''} />`}
              <span class="hint">${t('settings.password_hint')}</span>
            </div>
            <${TextField} label=${t('settings.from_email')} type="email" value=${f.from_email} onInput=${s.set('from_email')} placeholder="certs@example.com" error=${fe.from_email} />
            <${TextField} label=${t('settings.from_name')} value=${f.from_name} onInput=${s.set('from_name')} error=${fe.from_name} />
            <${NumberField} label=${t('settings.timeout')} value=${f.timeout_seconds} onInput=${s.set('timeout_seconds')} error=${fe.timeout_seconds} hint=${t('settings.seconds')} />
            <${TextField} label=${t('settings.helo')} value=${f.helo} onInput=${s.set('helo')} hint=${t('settings.helo_hint')} inputClass="mono" error=${fe.helo} />
          </div>
          <${Check} checked=${f.skip_verify} onChange=${s.set('skip_verify')} label=${t('settings.skip_verify')} hint=${t('settings.skip_verify_hint')} />
          ${f.security === 'none' && f.username && html`<${Callout} tone="warning" icon="alert">${t('settings.plaintext_warning')}<//>`}
          ${s.error && !Object.keys(fe).length && html`<${FormError} error=${s.error} />`}
        </div>
        <${SaveBar} busy=${s.busy} />
      <//>
      <${Card} title=${t('settings.test_title')} sub=${t('settings.test_sub')}>
        <div class="row-wrap" style=${{ alignItems: 'flex-end' }}>
          <div style=${{ flex: '1 1 260px' }}><${TextField} label=${t('settings.test_to')} type="email" value=${testTo} onInput=${setTestTo} /></div>
          <${Button} icon="send" loading=${testing} onClick=${test} disabled=${!testTo || !f.host}>${t('settings.send_test')}<//>
        </div>
        ${testResult && html`<div style=${{ marginTop: '14px' }}>${testResult.ok
          ? html`<${Callout} tone="ok" icon="checkCircle">${t('settings.test_ok', { ms: testResult.duration_ms })}<//>`
          : html`<${Callout} tone="expired" icon="xCircle"><b>${t('settings.test_failed')}</b> <span class="mono">${testResult.error}</span><//>`}</div>`}
      <//>
    </div>
  </form>`;
}

function Monitoring({ data, onSaved }) {
  const s = useSection(data.monitoring);
  const f = s.form;
  const fe = s.error?.fields || {};
  const [newT, setNewT] = useState('');
  const addThreshold = () => {
    const n = Number(newT);
    if (!Number.isInteger(n) || n < 1 || n > 365) return;
    s.setForm((x) => ({ ...x, thresholds: [...new Set([...x.thresholds, n])].sort((a, b) => b - a) }));
    setNewT('');
  };
  const presets = [
    { v: 60, l: t('settings.hourly') },
    { v: 180, l: t('settings.every', { what: tp('unit.hours', 3) }) },
    { v: 360, l: t('settings.every', { what: tp('unit.hours', 6) }) },
    { v: 720, l: t('settings.every', { what: tp('unit.hours', 12) }) },
    { v: 1440, l: t('settings.daily') },
  ];
  return html`<form onSubmit=${(e) => { e.preventDefault(); s.save('/settings/monitoring', { ...f,
      check_interval_minutes: Number(f.check_interval_minutes), check_timeout_seconds: Number(f.check_timeout_seconds),
      concurrency: Number(f.concurrency), critical_days: Number(f.critical_days), history_retention_days: Number(f.history_retention_days) }, onSaved); }}>
    <div class="stack">
      <${Card} title=${t('settings.alerts')} sub=${t('settings.alerts_sub')}>
        <div class="stack">
          <div class="field"><span class="label">${t('settings.thresholds')}</span>
            <div class="thresholds">
              ${f.thresholds.map((d) => html`<span class=${'threshold-chip tone-' + (d <= f.critical_days ? 'critical' : 'warning')}>
                ${tp('unit.days', d)}
                <button type="button" class="btn btn-ghost btn-xs btn-icon" aria-label=${t('common.remove')}
                  onClick=${() => s.setForm((x) => ({ ...x, thresholds: x.thresholds.filter((v) => v !== d) }))}><${Icon} name="x" /></button>
              </span>`)}
              <input class="input mono" style=${{ width: '86px', height: '30px' }} type="number" min="1" max="365" value=${newT}
                placeholder=${t('settings.days_ph')} onInput=${(e) => setNewT(e.target.value)}
                onKeyDown=${(e) => { if (e.key === 'Enter') { e.preventDefault(); addThreshold(); } }} />
              <${Button} size="sm" icon="plus" onClick=${addThreshold} disabled=${!newT}>${t('common.add')}<//>
              <${Button} size="sm" kind="ghost" onClick=${() => s.setForm((x) => ({ ...x, thresholds: [30, 14, 7, 3] }))}>${t('settings.reset_default')}<//>
            </div>
            ${fe.thresholds ? html`<div class="error">${t('field.' + fe.thresholds)}</div>` : html`<span class="hint">${t('settings.thresholds_hint')}</span>`}
          </div>
          <${Switch} checked=${f.notify_expired} onChange=${s.set('notify_expired')} label=${t('settings.notify_expired')} hint=${t('settings.notify_expired_hint')} />
          <div class="form-grid">
            <${NumberField} label=${t('settings.critical_days')} value=${f.critical_days} onInput=${s.set('critical_days')}
              hint=${t('settings.critical_days_hint')} error=${fe.critical_days} />
            <${NumberField} label=${t('settings.retention')} value=${f.history_retention_days} onInput=${s.set('history_retention_days')}
              hint=${t('settings.retention_hint')} error=${fe.history_retention_days} />
          </div>
        </div>
      <//>
      <${Card} title=${t('settings.checks')} sub=${t('settings.checks_sub')}>
        <div class="stack">
          <div class="field"><span class="label">${t('settings.interval')}</span>
            <div class="row-wrap">
              <${Segmented} value=${Number(f.check_interval_minutes)} onChange=${s.set('check_interval_minutes')} label=${t('settings.interval')}
                options=${presets.map((p) => ({ value: p.v, label: p.l }))} />
              <input class="input mono" type="number" min="5" max="10080" style=${{ width: '110px' }} value=${f.check_interval_minutes}
                onInput=${(e) => s.set('check_interval_minutes')(e.target.value === '' ? '' : Number(e.target.value))} aria-label=${t('settings.interval')} />
              <span class="muted">${t('settings.minutes')}</span>
            </div>
            ${fe.check_interval_minutes ? html`<div class="error">${t('field.' + fe.check_interval_minutes)}</div>` : html`<span class="hint">${t('settings.interval_hint')}</span>`}
          </div>
          <div class="form-grid">
            <${NumberField} label=${t('settings.check_timeout')} value=${f.check_timeout_seconds} onInput=${s.set('check_timeout_seconds')}
              hint=${t('settings.seconds')} error=${fe.check_timeout_seconds} />
            <${NumberField} label=${t('settings.concurrency')} value=${f.concurrency} onInput=${s.set('concurrency')}
              hint=${t('settings.concurrency_hint')} error=${fe.concurrency} />
          </div>
        </div>
        <${SaveBar} busy=${s.busy} />
      <//>
    </div>
  </form>`;
}

function Security({ data, onSaved }) {
  const s = useSection(data.security);
  const f = s.form;
  const fe = s.error?.fields || {};
  const p = f.password;
  const setP = (k) => (v) => s.setForm((x) => ({ ...x, password: { ...x.password, [k]: v } }));
  const num = (v) => (v === '' ? 0 : Number(v));
  const submit = (e) => {
    e.preventDefault();
    s.save('/settings/security', {
      ...f,
      password: { ...p, min_length: num(p.min_length), history_count: num(p.history_count), max_age_days: num(p.max_age_days) },
      max_failed_logins: num(f.max_failed_logins), lockout_minutes: num(f.lockout_minutes),
      session_lifetime_hours: num(f.session_lifetime_hours), session_idle_minutes: num(f.session_idle_minutes),
      invite_ttl_hours: num(f.invite_ttl_hours), reset_ttl_hours: num(f.reset_ttl_hours),
    }, onSaved);
  };
  return html`<form onSubmit=${submit}>
    <div class="stack">
      <${Card} title=${t('settings.pw_policy')} sub=${t('settings.pw_policy_sub')}>
        <div class="stack">
          <div class="form-grid">
            <${NumberField} label=${t('settings.pw_min_length')} value=${p.min_length} onInput=${setP('min_length')} error=${fe['password.min_length']} hint=${t('settings.pw_min_length_hint')} />
            <${NumberField} label=${t('settings.pw_history')} value=${p.history_count} onInput=${setP('history_count')} error=${fe['password.history_count']} hint=${t('settings.pw_history_hint')} />
            <${NumberField} label=${t('settings.pw_max_age')} value=${p.max_age_days} onInput=${setP('max_age_days')} error=${fe['password.max_age_days']} hint=${t('settings.pw_max_age_hint')} />
          </div>
          <div class="form-grid">
            <${Check} checked=${p.require_upper} onChange=${setP('require_upper')} label=${t('settings.pw_upper')} hint=${t('settings.pw_upper_hint')} />
            <${Check} checked=${p.require_lower} onChange=${setP('require_lower')} label=${t('settings.pw_lower')} hint=${t('settings.pw_lower_hint')} />
            <${Check} checked=${p.require_digit} onChange=${setP('require_digit')} label=${t('settings.pw_digit')} hint="0–9" />
            <${Check} checked=${p.require_special} onChange=${setP('require_special')} label=${t('settings.pw_special')} hint="! @ # $ % …" />
          </div>
          <${Callout} tone="accent" icon="info">${t('settings.pw_applies')}<//>
        </div>
      <//>
      <${Card} title=${t('settings.login_protection')} sub=${t('settings.login_protection_sub')}>
        <div class="form-grid">
          <${NumberField} label=${t('settings.max_failed')} value=${f.max_failed_logins} onInput=${s.set('max_failed_logins')} error=${fe.max_failed_logins} hint=${t('settings.max_failed_hint')} />
          <${NumberField} label=${t('settings.lockout')} value=${f.lockout_minutes} onInput=${s.set('lockout_minutes')} error=${fe.lockout_minutes} hint=${t('settings.minutes')} />
          <${NumberField} label=${t('settings.session_lifetime')} value=${f.session_lifetime_hours} onInput=${s.set('session_lifetime_hours')} error=${fe.session_lifetime_hours} hint=${t('settings.hours')} />
          <${NumberField} label=${t('settings.session_idle')} value=${f.session_idle_minutes} onInput=${s.set('session_idle_minutes')} error=${fe.session_idle_minutes} hint=${t('settings.session_idle_hint')} />
          <${NumberField} label=${t('settings.invite_ttl')} value=${f.invite_ttl_hours} onInput=${s.set('invite_ttl_hours')} error=${fe.invite_ttl_hours} hint=${t('settings.hours')} />
          <${NumberField} label=${t('settings.reset_ttl')} value=${f.reset_ttl_hours} onInput=${s.set('reset_ttl_hours')} error=${fe.reset_ttl_hours} hint=${t('settings.hours')} />
        </div>
        <${SaveBar} busy=${s.busy} />
      <//>
    </div>
  </form>`;
}

export function Settings({ params }) {
  const me = app.get().me;
  const tab = TABS.includes(params.tab) ? params.tab : 'general';
  const data = useAsync(() => get('/settings'), []);
  useTitle(t('nav.settings'), me.app.name);
  if (data.error) return html`<div class="page"><${ErrorState} error=${data.error} onRetry=${data.reload} /></div>`;
  if (!data.data) return html`<${PageLoader} />`;
  const onSaved = (r) => data.setData(r);
  const Section = { general: General, email: Email, monitoring: Monitoring, security: Security }[tab];
  return html`<div class="page" style=${{ maxWidth: '980px' }}>
    <${PageHead} title=${t('nav.settings')} sub=${t('settings.sub')} />
    <${Tabs} value=${tab} onChange=${(v) => navigate('/settings/' + v, { replace: true })} tabs=${[
      { value: 'general', label: t('settings.general'), icon: 'globe' },
      { value: 'email', label: t('settings.email'), icon: 'mail' },
      { value: 'monitoring', label: t('settings.monitoring'), icon: 'activity' },
      { value: 'security', label: t('settings.security_tab'), icon: 'lock' },
    ]} />
    <div class="reveal" key=${tab}><${Section} data=${data.data} onSaved=${onSaved} /></div>
  </div>`;
}
