// Shared presentation of a job: status text, progress bar and readouts.
// Components update their nodes in place so that focus, hover and scroll
// positions survive the frequent SSE progress events.

import { t } from './i18n.js';
import { h, fmt } from './dom.js';

export const isFinished = (j) => ['done', 'failed', 'canceled', 'expired'].includes(j.state);
export const isActive = (j) => ['pending', 'queued', 'running'].includes(j.state);

/** Derive the display model of a job. */
export function describe(j) {
  const p = j.progress || {};
  const total = p.percent > 0 && p.out_time ? p.out_time / (p.percent / 100) : 0;
  let status = t(`state.${j.state}`);
  let busy = false;
  let pct = p.percent || 0;
  switch (j.state) {
    case 'pending': status = t('job.pending'); busy = true; break;
    case 'queued': status = t('job.queued'); busy = true; break;
    case 'running':
      if (!p.out_time) { status = t('job.starting'); busy = true; } else if (pct) status = t('job.converting', { pct: Math.floor(pct) });
      else { status = t('job.convertingUnknown'); busy = true; }
      break;
    case 'ready': status = t('job.ready'); pct = 100; break;
    case 'done': status = t('job.done'); pct = 100; break;
    case 'failed': status = t('job.failed'); break;
    case 'canceled': status = t('job.canceled'); break;
    case 'expired': status = t('job.expired'); break;
  }
  return {
    status, busy, pct,
    led: `led led--${j.state}`,
    bar: ['bar', busy && 'bar--busy', (j.state === 'done' || j.state === 'ready') && 'bar--ok', ['failed', 'canceled', 'expired'].includes(j.state) && 'bar--off'].filter(Boolean).join(' '),
    time: `${fmt.duration(p.out_time)} / ${total ? fmt.duration(total) : '--:--'}`,
    speed: p.speed ? `${p.speed.toFixed(1)}×` : '',
    size: fmt.bytes(j.size || p.size),
  };
}

/** A status + progress block: {el, update(job)}. */
export function progressBlock() {
  const led = h('span');
  const pctEl = h('span', { class: 'mono small muted' });
  const fill = h('div', { class: 'bar__fill' });
  const bar = h('div', { class: 'bar' }, fill);
  const time = h('span');
  const speed = h('span');
  const size = h('span');
  const err = h('div', { class: 'job__err', hidden: true });
  const el = h('div', { class: 'prog' },
    h('div', { class: 'prog__top' }, led, h('span', { class: 'grow' }), pctEl),
    bar,
    h('div', { class: 'prog__stats' }, time, speed, size),
    err);
  return {
    el,
    update(j) {
      const d = describe(j);
      led.className = d.led;
      led.textContent = d.status;
      pctEl.textContent = d.busy || !d.pct ? '' : `${Math.floor(d.pct)}%`;
      bar.className = d.bar;
      fill.style.width = `${d.pct}%`;
      time.textContent = d.time;
      speed.textContent = d.speed;
      size.textContent = d.size;
      err.hidden = !j.error;
      err.textContent = j.error || '';
    },
  };
}
