// Thin fetch wrapper for the JSON API.
import { emit } from './store.js';

export class ApiError extends Error {
  constructor(status, body) {
    super(body?.message || `HTTP ${status}`);
    this.status = status;
    this.code = body?.code || 'http_' + status;
    this.fields = body?.fields || {};
    this.details = body?.details || {};
  }
}

export async function api(method, path, body) {
  const opts = { method, credentials: 'same-origin', headers: { Accept: 'application/json' } };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch('/api' + path, opts);
  } catch (e) {
    throw new ApiError(0, { code: 'network', message: e.message });
  }
  if (res.status === 204) return null;
  let data = null;
  try {
    data = await res.json();
  } catch {
    data = null;
  }
  if (!res.ok) {
    const err = new ApiError(res.status, data?.error);
    if (res.status === 401 && !path.startsWith('/auth/') && !path.startsWith('/setup')) emit('unauthorized');
    if (err.code === 'password_change_required') emit('password-change-required');
    throw err;
  }
  return data;
}

export const get = (p) => api('GET', p);
export const post = (p, b = {}) => api('POST', p, b);
export const put = (p, b = {}) => api('PUT', p, b);
export const del = (p) => api('DELETE', p);
