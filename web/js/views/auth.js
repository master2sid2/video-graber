// Authentication screens: first-run setup, login and invite registration.

import { t } from '../i18n.js';
import { h, mount, field } from '../dom.js';
import { get, post } from '../api.js';
import { loadStatus, loadPrefs } from '../state.js';

function shell(root, title, lead, form) {
  mount(root, h('div', { class: 'auth' }, h('div', { class: 'auth__card stagger' },
    h('div', { class: 'auth__brand' }, h('span', { class: 'brand__rec' }), h('span', { class: 'brand__word' }, 'Video', h('b', null, 'Graber'))),
    h('div', { class: 'slate' },
      h('div', { class: 'slate__clapper', aria: { hidden: 'true' } }),
      h('div', { class: 'slate__stripe', aria: { hidden: 'true' } }),
      h('div', { class: 'slate__body' }, h('h1', null, title), h('p', { class: 'lead' }, lead), form)))));
}

function authForm({ fields, submitLabel, onSubmit }) {
  const err = h('div', { class: 'form-error', hidden: true, role: 'alert' });
  const btn = h('button', { class: 'btn btn--rec btn--lg btn--block', type: 'submit' }, submitLabel);
  const form = h('form', { onsubmit: async (e) => {
    e.preventDefault();
    err.hidden = true;
    btn.disabled = true;
    try {
      await onSubmit();
    } catch (ex) {
      err.textContent = ex.message;
      err.hidden = false;
    } finally {
      btn.disabled = false;
    }
  } }, fields, err, btn);
  return form;
}

const input = (attrs) => h('input', { class: 'input', required: true, ...attrs });

async function finish() {
  await loadStatus();
  await loadPrefs();
  location.hash = '#/grab';
}

export const setup = {
  async render(root) {
    const token = input({ autocomplete: 'off', spellcheck: 'false', class: 'input mono' });
    const user = input({ autocomplete: 'username', minlength: 3, maxlength: 32 });
    const pw = input({ type: 'password', autocomplete: 'new-password', minlength: 8 });
    const pw2 = input({ type: 'password', autocomplete: 'new-password', minlength: 8 });
    shell(root, t('auth.setupTitle'), t('auth.setupLead'), authForm({
      fields: [field(t('auth.token'), token, t('auth.tokenHint')), field(t('auth.adminName'), user), field(t('auth.password'), pw), field(t('auth.confirm'), pw2)],
      submitLabel: t('auth.setupSubmit'),
      onSubmit: async () => {
        if (pw.value !== pw2.value) throw new Error(t('auth.mismatch'));
        await post('/api/setup', { token: token.value.trim(), username: user.value.trim(), password: pw.value });
        await finish();
      },
    }));
    token.focus();
  },
};

export const login = {
  async render(root) {
    const user = input({ autocomplete: 'username' });
    const pw = input({ type: 'password', autocomplete: 'current-password' });
    shell(root, t('auth.loginTitle'), t('auth.loginLead'), authForm({
      fields: [field(t('auth.username'), user), field(t('auth.password'), pw)],
      submitLabel: t('auth.loginSubmit'),
      onSubmit: async () => {
        await post('/api/login', { username: user.value.trim(), password: pw.value });
        await finish();
      },
    }));
    user.focus();
  },
};

export const register = {
  async render(root, { params }) {
    const code = params[0] || '';
    let valid = !!code;
    if (code) {
      try { await get(`/api/invites/${encodeURIComponent(code)}`); } catch { valid = false; }
    }
    if (!valid) {
      shell(root, t('auth.registerTitle'), t('auth.inviteInvalid'), h('a', { class: 'btn btn--block', href: '#/login' }, t('auth.toLogin')));
      return;
    }
    const user = input({ autocomplete: 'username', minlength: 3, maxlength: 32 });
    const pw = input({ type: 'password', autocomplete: 'new-password', minlength: 8 });
    const pw2 = input({ type: 'password', autocomplete: 'new-password', minlength: 8 });
    shell(root, t('auth.registerTitle'), t('auth.registerLead'), authForm({
      fields: [field(t('auth.username'), user, t('auth.usernameHint')), field(t('auth.password'), pw), field(t('auth.confirm'), pw2)],
      submitLabel: t('auth.registerSubmit'),
      onSubmit: async () => {
        if (pw.value !== pw2.value) throw new Error(t('auth.mismatch'));
        await post('/api/register', { invite: code, username: user.value.trim(), password: pw.value });
        await finish();
      },
    }));
    user.focus();
  },
};
