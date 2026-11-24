// Sign-in, first-run setup, invite acceptance, password reset.
import { html, useEffect, useState } from '../../vendor/preact-htm.js';
import { BrandMark, Dial } from '../components/icons.js';
import { Button, TextField, FormError, Callout, PageLoader } from '../components/ui.js';
import { PasswordField, policySatisfied } from '../components/pickers.js';
import { get, post } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { Link, navigate, useLocation } from '../lib/router.js';
import { t, fmtDateTime, errorText } from '../lib/i18n.js';
import { useTitle } from '../lib/hooks.js';
import { LanguageSwitch, logout } from '../components/layout.js';

// The build version for the footer, from the public health endpoint.
let versionReq;
function useVersion() {
  const [version, setVersion] = useState('');
  useEffect(() => {
    versionReq ||= get('/health').then((h) => h.version || '').catch(() => '');
    versionReq.then(setVersion);
  }, []);
  return version;
}

function AuthLayout({ children, appName }) {
  const version = useVersion();
  return html`<div class="auth">
    <aside class="auth-art">
      <div class="brand" style=${{ position: 'relative', zIndex: 1 }}>
        <${BrandMark} />
        <span><div class="brand-name">${appName || 'Cert Alerter'}</div><div class="brand-sub">${t('brand.tagline')}</div></span>
      </div>
      <${Dial} />
      <div class="claim">
        <h2>${t('auth.claim_title')}</h2>
        <p>${t('auth.claim_text')}</p>
      </div>
      <div class="stamp">TLS · X.509 · SMTP/TLS · 30 · 14 · 7 · 3</div>
    </aside>
    <section class="auth-form">
      <div class="auth-card reveal">${children}
        <div class="auth-foot">
          <span>© ${new Date().getFullYear()} ${appName || 'Cert Alerter'}${version && html`<span class="app-version" title=${t('common.version')}>${version}</span>`}</span>
          <${LanguageSwitch} compact=${false} size="xs" />
        </div>
      </div>
    </section>
  </div>`;
}

function nextPath(query) {
  const n = query.get('next') || '/';
  return n.startsWith('/') && !n.startsWith('//') ? n : '/';
}

export function Login() {
  const { query } = useLocation();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  useTitle(t('auth.sign_in'));
  useEffect(() => {
    get('/setup').then((s) => { if (s.setup_required) navigate('/setup', { replace: true }); }).catch(() => {});
  }, []);
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const me = await post('/auth/login', { email, password });
      app.set({ me });
      navigate(nextPath(query), { replace: true });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  const lockedUntil = error?.code === 'account_locked' && error.details?.locked_until;
  return html`<${AuthLayout}>
    <h1>${t('auth.welcome')}</h1>
    <p class="sub">${t('auth.sign_in_sub')}</p>
    <form onSubmit=${submit}>
      ${error && html`<${Callout} tone="expired" icon="alertCircle">
        ${errorText(error)}${lockedUntil && html` ${t('auth.locked_until', { time: fmtDateTime(lockedUntil) })}`}
      <//>`}
      <${TextField} label=${t('common.email')} type="email" autocomplete="username" required value=${email} onInput=${setEmail} />
      <${TextField} label=${t('auth.password')} type="password" autocomplete="current-password" required value=${password} onInput=${setPassword} />
      <${Button} kind="primary" type="submit" loading=${busy}>${t('auth.sign_in')}<//>
    </form>
    <div style=${{ marginTop: '16px', fontSize: '13px' }}><${Link} href="/forgot">${t('auth.forgot')}<//></div>
  <//>`;
}

export function Forgot() {
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState(null);
  useTitle(t('auth.forgot_title'));
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await post('/auth/forgot', { email });
      setSent(true);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return html`<${AuthLayout}>
    <h1>${t('auth.forgot_title')}</h1>
    <p class="sub">${t('auth.forgot_sub')}</p>
    ${sent
      ? html`<${Callout} tone="ok" icon="mail">${t('auth.forgot_sent')}<//>`
      : html`<form onSubmit=${submit}>
          <${FormError} error=${error} />
          <${TextField} label=${t('common.email')} type="email" required value=${email} onInput=${setEmail}
            error=${error?.fields?.email} />
          <${Button} kind="primary" type="submit" loading=${busy}>${t('auth.send_link')}<//>
        </form>`}
    <div style=${{ marginTop: '16px', fontSize: '13px' }}><${Link} href="/login">← ${t('auth.back_to_login')}<//></div>
  <//>`;
}

// TokenPage handles /invite/:token and /reset/:token.
export function TokenPage({ params }) {
  const [info, setInfo] = useState(null);
  const [loadError, setLoadError] = useState(null);
  const [name, setName] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  useEffect(() => {
    get('/auth/token/' + encodeURIComponent(params.token))
      .then((d) => { setInfo(d); setName(d.name || ''); })
      .catch(setLoadError);
  }, [params.token]);
  const invite = info?.purpose === 'invite';
  useTitle(invite ? t('auth.invite_title') : t('auth.reset_title'));
  if (loadError) {
    return html`<${AuthLayout}>
      <h1>${t('auth.link_invalid_title')}</h1>
      <p class="sub">${errorText(loadError)}</p>
      <${Callout} tone="warning" icon="clock">${t('auth.link_invalid_text')}<//>
      <div style=${{ marginTop: '18px' }}><${Link} href="/login" class="btn">${t('auth.back_to_login')}<//></div>
    <//>`;
  }
  if (!info) return html`<${AuthLayout}><${PageLoader} /><//>`;
  const mismatch = pw2 && pw !== pw2;
  const submit = async (e) => {
    e.preventDefault();
    if (pw !== pw2) return;
    setBusy(true);
    setError(null);
    try {
      const me = await post('/auth/token/' + encodeURIComponent(params.token), { name, password: pw });
      app.set({ me });
      toast(invite ? t('auth.invite_done') : t('auth.reset_done'));
      navigate('/', { replace: true });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return html`<${AuthLayout} appName=${info.app_name}>
    <h1>${invite ? t('auth.invite_title') : t('auth.reset_title')}</h1>
    <p class="sub">${invite ? t('auth.invite_sub', { email: info.email, role: t('role.' + info.role) }) : t('auth.reset_sub', { email: info.email })}</p>
    <form onSubmit=${submit}>
      ${error && error.code !== 'weak_password' && html`<${FormError} error=${error} />`}
      ${invite && html`<${TextField} label=${t('common.name')} value=${name} onInput=${setName} autocomplete="name" placeholder=${t('auth.name_placeholder')} />`}
      <${PasswordField} label=${t('auth.new_password')} value=${pw} onInput=${setPw} policy=${info.password_policy}
        email=${info.email} violations=${error?.details?.violations} />
      <${TextField} label=${t('auth.repeat_password')} type="password" autocomplete="new-password" value=${pw2} onInput=${setPw2}
        error=${mismatch ? 'mismatch' : ''} />
      <${Button} kind="primary" type="submit" loading=${busy}
        disabled=${!policySatisfied(pw, info.password_policy, info.email) || pw !== pw2}>
        ${invite ? t('auth.activate') : t('auth.set_password')}
      <//>
      <p class="muted" style=${{ fontSize: '12px' }}>${t('auth.link_valid_until', { time: fmtDateTime(info.expires_at) })}</p>
    </form>
  <//>`;
}

export function Setup() {
  const { query } = useLocation();
  const [status, setStatus] = useState(null);
  const [token, setToken] = useState(query.get('token') || '');
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  useTitle(t('setup.title'));
  useEffect(() => {
    get('/setup').then((s) => {
      if (!s.setup_required) navigate('/login', { replace: true });
      else setStatus(s);
    }).catch(setError);
  }, []);
  if (!status) return html`<${AuthLayout}><${PageLoader} /><//>`;
  const submit = async (e) => {
    e.preventDefault();
    if (pw !== pw2) return;
    setBusy(true);
    setError(null);
    try {
      const me = await post('/setup', { token: token.trim(), email, name, password: pw, base_url: location.origin });
      app.set({ me });
      toast(t('setup.done'));
      navigate('/', { replace: true });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return html`<${AuthLayout}>
    <h1>${t('setup.title')}</h1>
    <p class="sub">${t('setup.sub')}</p>
    <form onSubmit=${submit}>
      ${error && error.code !== 'weak_password' && html`<${FormError} error=${error} />`}
      <${TextField} label=${t('setup.token')} value=${token} onInput=${setToken} required inputClass="mono"
        hint=${t('setup.token_hint')} error=${error?.code === 'invalid_setup_token' ? 'invalid' : ''} />
      <${TextField} label=${t('common.email')} type="email" required value=${email} onInput=${setEmail} autocomplete="username"
        error=${error?.fields?.email} />
      <${TextField} label=${t('common.name')} value=${name} onInput=${setName} autocomplete="name" />
      <${PasswordField} label=${t('auth.password')} value=${pw} onInput=${setPw} policy=${status.password_policy} email=${email}
        violations=${error?.details?.violations} />
      <${TextField} label=${t('auth.repeat_password')} type="password" value=${pw2} onInput=${setPw2} autocomplete="new-password"
        error=${pw2 && pw !== pw2 ? 'mismatch' : ''} />
      <${Button} kind="primary" type="submit" loading=${busy}
        disabled=${!token || !policySatisfied(pw, status.password_policy, email) || pw !== pw2}>${t('setup.create_admin')}<//>
    </form>
  <//>`;
}

export function ForcePasswordChange() {
  const me = app.get().me;
  const [cur, setCur] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  useTitle(t('auth.expired_title'));
  const policy = me.app.password_policy;
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const next = await post('/me/password', { current_password: cur, new_password: pw });
      app.set({ me: next });
      toast(t('profile.password_changed'));
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return html`<${AuthLayout} appName=${me.app.name}>
    <h1>${t('auth.expired_title')}</h1>
    <p class="sub">${t('auth.expired_sub', { days: policy.max_age_days })}</p>
    <form onSubmit=${submit}>
      ${error && !['weak_password', 'validation'].includes(error.code) && html`<${FormError} error=${error} />`}
      <${TextField} label=${t('profile.current_password')} type="password" value=${cur} onInput=${setCur}
        autocomplete="current-password" error=${error?.fields?.current_password} />
      <${PasswordField} label=${t('auth.new_password')} value=${pw} onInput=${setPw} policy=${policy} email=${me.user.email}
        violations=${error?.details?.violations} />
      <${TextField} label=${t('auth.repeat_password')} type="password" value=${pw2} onInput=${setPw2} autocomplete="new-password"
        error=${pw2 && pw !== pw2 ? 'mismatch' : ''} />
      <${Button} kind="primary" type="submit" loading=${busy} disabled=${!cur || !policySatisfied(pw, policy, me.user.email) || pw !== pw2}>
        ${t('profile.change_password')}<//>
      <${Button} kind="ghost" onClick=${logout}>${t('nav.logout')}<//>
    </form>
  <//>`;
}
