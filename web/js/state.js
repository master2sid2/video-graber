// Application state: server status, live job list (SSE) and preferences.

import { get, put } from './api.js';
import { download } from './dom.js';

const listeners = new Set();

export const state = {
  status: null,  // /api/status
  jobs: new Map(),
  prefs: {},
};

export function subscribe(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function notify(kind) {
  for (const fn of listeners) fn(kind);
  document.body.classList.toggle('is-recording', [...state.jobs.values()].some((j) => j.state === 'running'));
}

export const user = () => state.status?.user || null;
export const isIndividual = () => state.status?.mode === 'individual';
export const caps = () => state.status?.capabilities || { containers: [], video_codecs: [], audio_codecs: [], presets: [], heights: [] };

export async function loadStatus() {
  state.status = await get('/api/status');
  return state.status;
}

// ── Jobs (Server-Sent Events) ──────────────────────────────────────────────

let source = null;
// Jobs created by this tab that should be downloaded automatically when ready.
const autoDownload = new Set();
// Stream jobs waiting to be started (spaced out so browsers accept them).
const streamQueue = [];
let streamTimer = null;

export function connectEvents() {
  if (source) return;
  source = new EventSource('/api/events');
  const es = source;
  // The browser gives up for good after a non-200 reconnect (401, 502…).
  es.onerror = () => {
    if (es.readyState === EventSource.CLOSED && source === es) {
      source = null;
      setTimeout(connectEvents, 3000);
    }
  };
  source.addEventListener('snapshot', (e) => {
    state.jobs.clear();
    for (const j of JSON.parse(e.data)) state.jobs.set(j.id, j);
    notify('jobs');
  });
  source.addEventListener('job', (e) => {
    const { job } = JSON.parse(e.data);
    state.jobs.set(job.id, job);
    if (job.state === 'ready' && autoDownload.has(job.id)) {
      autoDownload.delete(job.id);
      download(`/api/jobs/${job.id}/download`);
    }
    if (job.state === 'failed' || job.state === 'canceled') autoDownload.delete(job.id);
    notify('jobs');
  });
  source.addEventListener('removed', (e) => {
    const { id } = JSON.parse(e.data);
    state.jobs.delete(id);
    notify('jobs');
  });
}

export function disconnectEvents() {
  source?.close();
  source = null;
  state.jobs.clear();
}

/** Start delivering freshly created jobs to the browser. */
export function deliver(jobs) {
  for (const j of jobs) {
    state.jobs.set(j.id, j);
    if (j.mode === 'file') autoDownload.add(j.id);
    else streamQueue.push(j.id);
  }
  notify('jobs');
  pumpStreams();
}

function pumpStreams() {
  if (streamTimer || !streamQueue.length) return;
  const id = streamQueue.shift();
  download(`/api/jobs/${id}/download`);
  streamTimer = setTimeout(() => { streamTimer = null; pumpStreams(); }, 900);
}

export function activeCount() {
  let n = 0;
  for (const j of state.jobs.values()) if (['pending', 'queued', 'running', 'ready'].includes(j.state)) n++;
  return n;
}

// ── Preferences ────────────────────────────────────────────────────────────

const PREFS_KEY = 'vg.prefs';

export async function loadPrefs() {
  if (isIndividual() && user()) {
    try { state.prefs = (await get('/api/me/prefs')) || {}; } catch { state.prefs = {}; }
  } else {
    try { state.prefs = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}'); } catch { state.prefs = {}; }
  }
  return state.prefs;
}

export async function savePrefs(patch) {
  state.prefs = { ...state.prefs, ...patch };
  if (isIndividual() && user()) {
    try { await put('/api/me/prefs', state.prefs); } catch { /* best effort */ }
  } else {
    localStorage.setItem(PREFS_KEY, JSON.stringify(state.prefs));
  }
}
