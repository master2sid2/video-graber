// Administration: one full-height card with tabs (users · invites · cookies).

import { t } from '../i18n.js';
import { h, icon, mount, toast, modal, confirmDialog, field, fmt, copyText } from '../dom.js';
import { get, post, patch, del } from '../api.js';
import { user } from '../state.js';

let tab = 'users';

export async function render(root) {
  const pane = h('div', { class: 'vbody' });
  const tabs = h('div', { class: 'tabs', role: 'tablist' });
  const names = ['users', 'invites'];
  const show = (name) => {
    tab = name;
    for (const b of tabs.children) b.setAttribute('aria-selected', String(b.dataset.tab === name));
    ({ users: usersPane, invites: invitesPane })[name](pane);
  };
  for (const n of names) tabs.append(h('button', { type: 'button', role: 'tab', dataset: { tab: n }, onclick: () => show(n) }, t(`admin.tab.${n}`)));
  mount(root, h('div', { class: 'vhead' }, h('h1', null, t('admin.title')), h('span', { class: 'grow' }), tabs), pane);
  show(names.includes(tab) ? tab : 'users');
}

function tableCard(title, action, head, tbody, intro) {
  return h('section', { class: 'card', style: { flex: '1' } },
    h('div', { class: 'card__head' }, h('h2', { class: 'card__title grow' }, title), action),
    intro ? h('div', { style: { padding: '12px 16px 0' } }, h('p', { class: 'muted', style: { margin: '0' } }, intro)) : null,
    h('div', { class: 'card__body card__body--flush' }, h('table', { class: 'table' }, h('thead', null, h('tr', null, head.map((x) => h('th', null, x)))), tbody)));
}

async function usersPane(pane) {
  const tbody = h('tbody');
  mount(pane, tableCard(t('admin.users'), h('button', { class: 'btn btn--sm', onclick: createUser }, icon('plus'), t('admin.addUser')),
    [t('admin.username'), t('admin.role'), t('admin.status'), t('admin.created'), t('admin.lastLogin'), ''], tbody));

  async function load() {
    try {
      const users = await get('/api/admin/users');
      const me = user();
      mount(tbody, users.map((u) => h('tr', null,
        h('td', null, h('b', null, u.username), u.id === me.id ? h('span', { class: 'muted small' }, ` (${t('admin.you')})`) : null),
        h('td', null, h('span', { class: ['tag', u.is_admin && 'tag--accent'] }, u.is_admin ? t('admin.admin') : t('admin.member'))),
        h('td', null, h('span', { class: ['tag', !u.disabled && 'tag--ok'] }, u.disabled ? t('admin.disabled') : t('admin.active'))),
        h('td', { class: 'small muted nowrap' }, fmt.date(u.created_at)),
        h('td', { class: 'small muted nowrap' }, u.last_login_at ? fmt.date(u.last_login_at) : '—'),
        h('td', null, u.id === me.id ? null : h('div', { class: 'actions' },
          h('button', { class: 'btn btn--ghost btn--sm', onclick: () => update(u, { is_admin: !u.is_admin }) }, u.is_admin ? t('admin.demote') : t('admin.promote')),
          h('button', { class: 'btn btn--ghost btn--sm', onclick: () => update(u, { disabled: !u.disabled }) }, u.disabled ? t('admin.enable') : t('admin.disable')),
          h('button', { class: 'btn btn--ghost btn--sm', onclick: () => resetPassword(u) }, t('admin.resetPassword')),
          h('button', { class: 'btn btn--ghost btn--icon btn--sm btn--danger', title: t('common.delete'), aria: { label: t('common.delete') }, onclick: async () => {
            if (!(await confirmDialog(t('admin.deleteUser', { name: u.username })))) return;
            try { await del(`/api/admin/users/${u.id}`); load(); } catch (e) { toast(e.message, 'error'); }
          } }, icon('trash')))))));
    } catch (e) { toast(e.message, 'error'); }
  }

  async function update(u, body) {
    try { await patch(`/api/admin/users/${u.id}`, body); load(); } catch (e) { toast(e.message, 'error'); }
  }

  async function resetPassword(u) {
    const pw = h('input', { class: 'input', type: 'password', minlength: 8, required: true, autocomplete: 'new-password' });
    await modal({
      title: t('admin.resetPasswordFor', { name: u.username }),
      body: [field(t('settings.newPassword'), pw, t('admin.resetHint'))],
      actions: [{ label: t('common.cancel'), kind: 'ghost' }, { label: t('common.save'), kind: 'sel', submit: true }],
      onSubmit: async () => {
        try { await patch(`/api/admin/users/${u.id}`, { password: pw.value }); toast(t('admin.passwordReset'), 'ok'); return true; } catch (e) { toast(e.message, 'error'); return false; }
      },
    });
  }

  async function createUser() {
    const name = h('input', { class: 'input', required: true, autocomplete: 'off' });
    const pw = h('input', { class: 'input', type: 'password', required: true, minlength: 8, autocomplete: 'new-password' });
    const adm = h('input', { type: 'checkbox' });
    await modal({
      title: t('admin.addUser'),
      body: [field(t('admin.username'), name, t('auth.usernameHint')), field(t('auth.password'), pw), h('label', { class: 'check' }, adm, t('admin.makeAdmin'))],
      actions: [{ label: t('common.cancel'), kind: 'ghost' }, { label: t('common.create'), kind: 'sel', submit: true }],
      onSubmit: async () => {
        try { await post('/api/admin/users', { username: name.value.trim(), password: pw.value, is_admin: adm.checked }); return true; } catch (e) { toast(e.message, 'error'); return false; }
      },
    });
    load();
  }

  await load();
}

const inviteURL = (code) => `${location.origin}${location.pathname}#/register/${encodeURIComponent(code)}`;

async function invitesPane(pane) {
  const tbody = h('tbody');
  mount(pane, tableCard(t('admin.invites'), h('button', { class: 'btn btn--sm', onclick: createInvite }, icon('plus'), t('admin.addInvite')),
    [t('admin.link'), t('admin.note'), t('admin.uses'), t('admin.expires'), ''], tbody, t('admin.invitesIntro')));

  async function load() {
    try {
      const list = await get('/api/admin/invites');
      if (!list.length) {
        mount(tbody, h('tr', null, h('td', { colspan: 5, class: 'muted' }, t('admin.noInvites'))));
        return;
      }
      mount(tbody, list.map((i) => h('tr', null,
        h('td', { style: { width: '45%' } }, h('div', { class: 'code' }, h('span', null, inviteURL(i.code)),
          h('button', { class: 'btn btn--ghost btn--icon btn--sm', title: t('admin.copy'), aria: { label: t('admin.copy') },
            onclick: async () => toast((await copyText(inviteURL(i.code))) ? t('admin.copied') : t('admin.copyFailed'), 'ok') }, icon('copy')))),
        h('td', null, i.note || h('span', { class: 'muted' }, '—')),
        h('td', { class: 'mono small' }, `${i.uses} / ${i.max_uses > 0 ? i.max_uses : '∞'}`),
        h('td', { class: 'small nowrap' }, i.usable ? (i.expires_at ? fmt.date(i.expires_at) : t('admin.never')) : h('span', { class: 'tag' }, t('admin.inviteUsed'))),
        h('td', null, h('div', { class: 'actions' }, h('button', { class: 'btn btn--ghost btn--icon btn--sm btn--danger', title: t('common.delete'), aria: { label: t('common.delete') },
          onclick: async () => { try { await del(`/api/admin/invites/${i.id}`); load(); } catch (e) { toast(e.message, 'error'); } } }, icon('trash')))))));
    } catch (e) { toast(e.message, 'error'); }
  }

  async function createInvite() {
    const note = h('input', { class: 'input', maxlength: 200, placeholder: t('admin.notePlaceholder') });
    const uses = h('input', { class: 'input mono', type: 'number', min: 0, value: 1 });
    const ttl = h('input', { class: 'input mono', type: 'number', min: 0, value: 72 });
    let created = null;
    await modal({
      title: t('admin.addInvite'),
      body: [field(t('admin.note'), note), h('div', { class: 'grid2' }, field(t('admin.maxUses'), uses, t('admin.zeroUnlimited')), field(t('admin.ttlHours'), ttl, t('admin.zeroNever')))],
      actions: [{ label: t('common.cancel'), kind: 'ghost' }, { label: t('common.create'), kind: 'sel', submit: true }],
      onSubmit: async () => {
        try { created = await post('/api/admin/invites', { note: note.value, max_uses: Number(uses.value) || 0, ttl_hours: Number(ttl.value) || 0 }); return true; } catch (e) { toast(e.message, 'error'); return false; }
      },
    });
    if (created) {
      const ok = await copyText(inviteURL(created.code));
      toast(ok ? t('admin.inviteCreatedCopied') : t('admin.inviteCreated'), 'ok');
    }
    load();
  }

  await load();
}
