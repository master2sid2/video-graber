// Queue view: live list of jobs with progress readouts.

import { t } from '../i18n.js';
import { h, icon, mount, thumb, fmt, toast, download, confirmDialog } from '../dom.js';
import { post, del } from '../api.js';
import { state, subscribe } from '../state.js';

export async function render(root) {
  const list = h('div', { class: 'reels' });
  const clearBtn = h('button', { class: 'btn btn--ghost btn--sm', onclick: clearFinished }, icon('trash'), t('queue.clearFinished'));
  mount(root,
    h('div', { class: 'page-head' },
      h('h1', { class: 'page-title' }, h('small', null, t('queue.eyebrow')), t('queue.title')),
      clearBtn),
    list);

  const nodes = new Map();
  const draw = () => {
    const jobs = [...state.jobs.values()].sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
    clearBtn.hidden = !jobs.some(isFinished);
    if (!jobs.length) {
      nodes.clear();
      mount(list, h('div', { class: 'empty' }, icon('queue'), h('h3', null, t('queue.emptyTitle')),
        h('p', null, t('queue.emptyText')), h('a', { class: 'btn btn--amber', href: '#/grab' }, t('queue.goGrab'))));
      return;
    }
    if (list.querySelector('.empty')) list.replaceChildren();
    const seen = new Set();
    jobs.forEach((j, i) => {
      seen.add(j.id);
      const prev = nodes.get(j.id);
      let node = prev?.node;
      // state.js replaces only the job objects that changed: rebuild just those,
      // so focus and clicks on other reels are not disturbed.
      if (!prev || prev.job !== j) {
        const fresh = reel(j);
        if (node) {
          fresh.style.animation = 'none'; // only animate newly added reels
          const hadFocus = node.contains(document.activeElement);
          node.replaceWith(fresh);
          if (hadFocus) fresh.querySelector('button')?.focus();
        }
        node = fresh;
        nodes.set(j.id, { node, job: j });
      }
      if (list.children[i] !== node) list.insertBefore(node, list.children[i] || null);
    });
    for (const [id, v] of nodes) if (!seen.has(id)) { v.node.remove(); nodes.delete(id); }
  };
  draw();
  return subscribe((kind) => { if (kind === 'jobs') draw(); });
}

function isFinished(j) { return ['done', 'failed', 'canceled', 'expired'].includes(j.state); }

async function clearFinished() {
  const done = [...state.jobs.values()].filter(isFinished);
  await Promise.allSettled(done.map((j) => del(`/api/jobs/${j.id}`)));
}

function reel(j) {
  const p = j.progress || {};
  const total = p.percent > 0 && p.out_time ? p.out_time / (p.percent / 100) : 0;
  const active = j.state === 'running' || j.state === 'queued';
  const indeterminate = j.state === 'running' && !p.percent;
  const bar = h('div', { class: ['progress', indeterminate && 'progress--indeterminate', (j.state === 'done' || j.state === 'ready') && 'progress--done'] },
    h('div', { class: 'progress__fill', style: { width: `${j.state === 'done' || j.state === 'ready' ? 100 : (p.percent || 0)}%` } }));

  const actions = [];
  if (j.state === 'ready') {
    actions.push(h('button', { class: 'btn btn--amber btn--sm', onclick: () => download(`/api/jobs/${j.id}/download`) }, icon('grab'), t('queue.save')));
  }
  if (j.mode === 'stream' && ['pending', 'failed', 'canceled', 'done'].includes(j.state)) {
    actions.push(h('button', { class: 'btn btn--sm', title: t('queue.restartHint'), onclick: () => download(`/api/jobs/${j.id}/download`) },
      icon(j.state === 'pending' ? 'play' : 'refresh'), j.state === 'pending' ? t('queue.start') : t('queue.again')));
  }
  if (active) {
    actions.push(h('button', { class: 'btn btn--sm btn--danger', onclick: () => post(`/api/jobs/${j.id}/cancel`).catch((e) => toast(e.message, 'error')) }, icon('stop'), t('queue.cancel')));
  }
  actions.push(h('button', {
    class: 'btn btn--ghost btn--icon', title: t('queue.remove'), aria: { label: t('queue.remove') },
    onclick: async () => {
      if (active && !(await confirmDialog(t('queue.removeActive')))) return;
      del(`/api/jobs/${j.id}`).catch((e) => toast(e.message, 'error'));
    },
  }, icon('x')));

  const out = j.output || {};
  return h('article', { class: 'reel', dataset: { id: j.id } },
    h('div', { class: 'reel__thumb' }, thumb(j.thumbnail)),
    h('div', { style: { minWidth: '0' } },
      h('h3', { class: 'reel__title', title: j.title }, j.title || j.filename),
      h('div', { class: 'reel__sub', title: j.filename }, j.subtitle ? `${j.subtitle} — ${j.filename}` : j.filename),
      bar,
      h('div', { class: 'reel__info' },
        h('span', { class: `led led--${j.state}` }, t(`state.${j.state}`)),
        h('span', null, h('b', null, fmt.duration(p.out_time)), ' / ', total ? fmt.duration(total) : '--:--'),
        p.speed ? h('span', null, t('queue.speed'), ' ', h('b', null, `${p.speed.toFixed(1)}×`)) : null,
        h('span', null, h('b', null, fmt.bytes(j.size || p.size))),
        h('span', null, String(out.container || '').toUpperCase(), out.height ? ` · ${out.height}p` : '', ' · ', t(`grab.mode${j.mode === 'file' ? 'File' : 'Stream'}`))),
      j.error ? h('div', { class: 'reel__err' }, j.error) : null,
      j.state === 'pending' ? h('div', { class: 'hint', style: { marginTop: '6px' } }, t('queue.pendingHint')) : null),
    h('div', { class: 'reel__actions' }, actions));
}
