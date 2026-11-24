// Profile (individual mode): account details, password change, sign out.

import { t } from '../i18n.js';
import { h, icon, mount, toast, field, fmt } from '../dom.js';
import { put, post } from '../api.js';
import { user, loadStatus, disconnectEvents } from '../state.js';

export async function render(root, { navigate }) {
  const u = user();
  const cur = h('input', { class: 'input', type: 'password', autocomplete: 'current-password', required: true });
  const nw = h('input', { class: 'input', type: 'password', autocomplete: 'new-password', required: true, minlength: 8 });
  const nw2 = h('input', { class: 'input', type: 'password', autocomplete: 'new-password', required: true, minlength: 8 });
  const err = h('div', { class: 'form-error', hidden: true, role: 'alert' });

  const password = h('form', { class: 'card', onsubmit: async (e) => {
    e.preventDefault();
    err.hidden = true;
    if (nw.value !== nw2.value) { err.textContent = t('auth.mismatch'); err.hidden = false; return; }
    try {
      await put('/api/me/password', { current: cur.value, new: nw.value });
      password.reset();
      toast(t('settings.passwordChanged'), 'ok');
    } catch (ex) { err.textContent = ex.message; err.hidden = false; }
  } },
  h('div', { class: 'card__head' }, h('h2', { class: 'card__title' }, t('profile.password'))),
  h('div', { class: 'card__body' },
    h('p', { class: 'muted', style: { margin: '0' } }, t('profile.passwordIntro')),
    field(t('settings.currentPassword'), cur),
    h('div', { class: 'grid2' }, field(t('settings.newPassword'), nw, t('profile.passwordRule')), field(t('auth.confirm'), nw2)),
    err),
  h('div', { class: 'card__foot' }, h('span', { class: 'grow' }), h('button', { class: 'btn btn--sel btn--sm', type: 'submit' }, icon('check'), t('settings.changePassword'))));

  const signOut = async () => {
    try { await post('/api/logout'); } catch { /* ignore */ }
    disconnectEvents();
    await loadStatus();
    navigate('/login');
  };

  const profile = h('section', { class: 'card' },
    h('div', { class: 'card__head' }, h('h2', { class: 'card__title' }, t('profile.account'))),
    h('div', { class: 'card__body' },
      h('div', { class: 'profile' },
        h('span', { class: 'avatar avatar--lg', aria: { hidden: 'true' } }, u.username.slice(0, 2).toUpperCase()),
        h('div', { style: { minWidth: '0' } },
          h('div', { class: 'profile__name' }, u.username),
          h('span', { class: ['tag', u.is_admin && 'tag--accent'] }, u.is_admin ? t('account.admin') : t('account.member')))),
      h('dl', { class: 'kv' },
        h('dt', null, t('profile.created')), h('dd', null, fmt.date(u.created_at)),
        h('dt', null, t('profile.lastLogin')), h('dd', null, u.last_login_at ? fmt.date(u.last_login_at) : '—')),
      h('div', { class: 'row' },
        h('a', { class: 'btn btn--sm', href: '#/settings' }, icon('settings'), t('profile.toSettings')),
        h('a', { class: 'btn btn--sm', href: '#/library' }, icon('library'), t('profile.toLibrary')))),
    h('div', { class: 'card__foot' }, h('span', { class: 'grow' }),
      h('button', { class: 'btn btn--ghost btn--danger btn--sm', type: 'button', onclick: signOut }, icon('logout'), t('account.logout'))));

  mount(root,
    h('div', { class: 'vhead' }, h('h1', null, t('profile.title'))),
    h('div', { class: 'vbody' }, h('div', { class: 'tiles tiles--profile' }, profile, password)));
}
