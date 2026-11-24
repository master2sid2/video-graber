// Grab view: URL bar on top, three evenly sized columns below
// (Media · Source · Output). Every column scrolls on its own; interactions
// update the affected nodes in place so lists never jump back to the top.

import { t } from '../i18n.js';
import { h, icon, mount, toast, thumb, fmt, segmented, selectEl, field, keepScroll, copyText } from '../dom.js';
import { get, post } from '../api.js';
import { state, caps, isIndividual, user, savePrefs } from '../state.js';
import { openProgress } from '../progress.js';

// Survives navigation between views within the browser session.
const session = {
  url: '',
  extractor: 'auto',
  result: null,      // {id, media, stream}
  stream: null,
  selection: { translation: '', episode: '' },
  marked: new Set(),
  multi: false,
  season: '',
  out: null,
  advanced: false,
  tab: 'media',
  categories: [],
};

function defaults() {
  const p = state.prefs || {};
  return {
    audioOnly: false,
    container: p.container || 'mp4',
    audioContainer: p.audioContainer || 'mp3',
    videoCodec: p.videoCodec || '',
    audioCodec: '',
    height: p.height || 0,
    crf: 0,
    preset: '',
    audioBitrate: p.audioBitrate || 0,
    start: '',
    end: '',
    mode: p.mode || 'stream',
    format: '',
    audio: '',
    subtitles: new Set(),
    categoryId: p.categoryId || '',
  };
}

let ui = null; // nodes of the current render

export async function render(root, { query, navigate }) {
  session.out ||= defaults();
  const body = h('div', { class: 'vbody' });
  ui = { root, body, navigate };
  mount(root, urlbar(), body);

  if (isIndividual() && user()) {
    get('/api/categories').then((c) => {
      session.categories = c || [];
      if (ui?.output) renderOutput();
    }).catch(() => {});
  }

  const qURL = query.get('url');
  if (qURL && qURL !== session.url) {
    session.url = qURL;
    ui.input.value = qURL;
    if (query.get('extractor')) session.extractor = query.get('extractor');
    history.replaceState(null, '', '#/grab');
    runGrab();
  } else if (session.result) {
    renderResult();
  } else {
    renderIntro();
  }
  return () => { ui = null; };
}

// ── URL bar ────────────────────────────────────────────────────────────────

function urlbar() {
  const extractors = (state.status.extractors || ['auto']).map((e) => ({ value: e, label: t(`extractor.${e}`) }));
  const input = h('input', {
    class: 'input', type: 'url', inputmode: 'url', autocomplete: 'off', spellcheck: 'false', required: true,
    placeholder: t('grab.placeholder'), value: session.url, aria: { label: t('grab.urlLabel') },
  });
  const btn = h('button', { class: 'btn btn--primary', type: 'submit' }, icon('bolt'), h('span', null, t('grab.go')));
  ui.input = input;
  ui.grabBtn = btn;
  return h('form', { class: 'urlbar', onsubmit: (e) => { e.preventDefault(); session.url = input.value.trim(); runGrab(); } },
    h('div', { class: 'urlbar__input' }, icon('link'), input),
    selectEl(extractors, session.extractor, (v) => { session.extractor = v; }, { aria: { label: t('grab.extractor') } }),
    btn);
}

function renderIntro() {
  const st = state.status;
  const on = (b) => h('span', { class: ['tag', b && 'tag--ok'] }, b ? t('common.on') : t('common.off'));
  const tile = (ic, title, text, extra) => h('div', { class: 'card' }, h('div', { class: 'card__body' },
    icon(ic), h('h3', null, title), h('p', null, text), extra));
  mount(ui.body, h('div', { class: 'intro' },
    tile('film', t('grab.intro1Title'), t('grab.intro1Text'),
      h('div', { class: 'row' }, h('span', { class: 'small muted' }, 'yt-dlp'), on(st.features.ytdlp), h('span', { class: 'small muted' }, t('grab.browser')), on(st.features.browser))),
    tile('queue', t('grab.intro2Title'), t('grab.intro2Text')),
    tile('music', t('grab.intro3Title'), t('grab.intro3Text'))));
}

function renderLoading() {
  const sk = (hgt) => h('div', { class: 'skel', style: { height: hgt } });
  const col = (rows) => h('div', { class: 'card' }, h('div', { class: 'card__head' }, h('div', { class: 'skel', style: { height: '12px', width: '40%' } })),
    h('div', { class: 'card__body' }, rows));
  mount(ui.body, h('div', { class: 'grab-grid is-loading', aria: { busy: 'true', label: t('grab.scanning') } },
    col([h('div', { class: 'media-info' }, sk('168px'), h('div', { style: { display: 'grid', gap: '8px', alignContent: 'start' } }, sk('18px'), sk('18px'), sk('12px'))), sk('30px'), sk('200px')]),
    col([sk('36px'), sk('46px'), sk('46px'), sk('46px'), h('p', { class: 'hint', style: { textAlign: 'center' } }, t('grab.scanning'))]),
    col([sk('30px'), sk('36px'), sk('36px'), sk('36px')])));
}

async function runGrab() {
  if (!session.url || !ui) return;
  const btn = ui.grabBtn;
  btn.disabled = true;
  btn.replaceChildren(h('span', { class: 'spinner' }), h('span', null, t('grab.scanningShort')));
  renderLoading();
  try {
    const res = await post('/api/extract', { url: session.url, extractor: session.extractor === 'auto' ? '' : session.extractor });
    session.result = res;
    session.stream = res.stream;
    session.selection = { ...res.media.selected };
    session.marked = new Set();
    session.multi = false;
    session.season = seasonOf(res.media, res.media.selected.episode);
    session.out = { ...defaults(), categoryId: session.out?.categoryId || '' };
    session.tab = 'media';
    if (ui) renderResult();
  } catch (err) {
    if (ui) renderFailure(err);
  } finally {
    btn.disabled = false;
    btn.replaceChildren(icon('bolt'), h('span', null, t('grab.go')));
  }
}

// Failure card. "unsupported" means every module was tried and none
// recognised the page: the user is asked to report the site.
function renderFailure(err) {
  const data = err.data || {};
  if (data.code !== 'unsupported') {
    mount(ui.body, h('div', { class: 'card', style: { flex: '1' } }, h('div', { class: 'state state--error' }, icon('x'),
      h('h3', null, t('grab.failed')), h('p', null, err.message))));
    return;
  }
  const report = state.status.report_url;
  const reportHref = report ? report.replaceAll('{url}', encodeURIComponent(session.url)) : '';
  const actions = h('div', { class: 'row', style: { justifyContent: 'center' } });
  if (reportHref) {
    actions.append(h('a', { class: 'btn btn--primary', href: reportHref, target: '_blank', rel: 'noopener noreferrer' }, icon('link'), t('grab.report')));
  }
  actions.append(h('button', { class: 'btn', type: 'button', onclick: async () => {
    const ok = await copyText(session.url);
    toast(ok ? t('grab.linkCopied') : t('admin.copyFailed'), ok ? 'ok' : 'error');
  } }, icon('copy'), t('grab.copyLink')));
  if (state.status.features.browser && session.extractor !== 'browser') {
    actions.append(h('button', { class: 'btn btn--ghost', type: 'button', onclick: () => { session.extractor = 'browser'; ui.root.querySelector('.urlbar select').value = 'browser'; runGrab(); } }, icon('refresh'), t('grab.tryBrowser')));
  }
  const detailOpen = { v: false };
  const detail = h('p', { class: 'mono small', hidden: true, style: { maxWidth: '640px', overflowWrap: 'anywhere' } }, data.detail || '');
  mount(ui.body, h('div', { class: 'card', style: { flex: '1' } }, h('div', { class: 'state' }, icon('search'),
    h('h3', null, t('grab.unsupportedTitle')),
    h('p', null, reportHref ? t('grab.unsupportedText') : t('grab.unsupportedTextNoLink')),
    h('div', { class: 'chips', style: { justifyContent: 'center', maxWidth: '640px' } },
      (data.tried || []).map((m) => h('span', { class: 'tag' }, m))),
    actions,
    data.detail ? h('button', { class: 'btn btn--ghost btn--sm', type: 'button', onclick: (e) => {
      detailOpen.v = !detailOpen.v;
      detail.hidden = !detailOpen.v;
      e.currentTarget.textContent = detailOpen.v ? t('grab.hideDetails') : t('grab.showDetails');
    } }, t('grab.showDetails')) : null,
    detail)));
}

function seasonOf(media, epID) {
  return media.episodes?.find((e) => e.id === epID)?.season || media.episodes?.[0]?.season || '';
}

// ── Result layout ──────────────────────────────────────────────────────────

function column(cls, title) {
  const n = h('span', { class: 'n' });
  const extra = h('div', { class: 'row', style: { marginLeft: 'auto' } });
  const body = h('div', { class: 'card__body' });
  const card = h('section', { class: ['card', cls] },
    h('div', { class: 'card__head' }, h('h2', { class: 'card__title' }, title, n), extra), body);
  return { card, body, n, extra };
}

function renderResult() {
  const media = column('col-media', t('grab.media'));
  const source = column('col-source', t('grab.source'));
  const output = column('col-output', t('grab.output'));
  output.foot = h('div', { class: 'card__foot' });
  output.card.append(output.foot);
  ui.media = media;
  ui.source = source;
  ui.output = output;
  ui.resolving = false;

  const tabs = h('div', { class: 'tabs grab-tabs', role: 'tablist' });
  const cols = { media: media.card, source: source.card, output: output.card };
  const setTab = (name) => {
    session.tab = name;
    for (const [k, el] of Object.entries(cols)) el.classList.toggle('is-active', k === name);
    for (const b of tabs.children) b.setAttribute('aria-selected', String(b.dataset.tab === name));
  };
  for (const name of ['media', 'source', 'output']) {
    tabs.append(h('button', { type: 'button', role: 'tab', dataset: { tab: name }, onclick: () => { setTab(name); window.scrollTo({ top: 0 }); } }, t(`grab.${name}`)));
  }
  mount(ui.body, tabs, h('div', { class: 'grab-grid has-sticky' }, media.card, source.card, output.card));
  setTab(session.tab);
  renderMedia();
  renderSource();
  renderOutput();
}

// ── Media column ───────────────────────────────────────────────────────────

function renderMedia() {
  const col = ui.media;
  const { media } = session.result;
  const st = session.stream;
  col.extra.replaceChildren(h('span', { class: 'tag' }, media.extractor));
  const facts = [
    st?.duration || media.duration ? fmt.duration(st?.duration || media.duration) : null,
    media.episodes?.length ? t('grab.nEpisodes', { count: media.episodes.length }) : null,
    media.translations?.length ? t('grab.nTranslations', { count: media.translations.length }) : null,
  ].filter(Boolean);

  keepScroll(col.body, () => mount(col.body,
    h('div', { class: 'media-info' },
      h('div', { class: 'poster' }, thumb(media.thumbnail, media.title)),
      h('div', { style: { minWidth: '0' } },
        h('h3', { class: 'media-title', title: media.title }, media.title || t('grab.untitled')),
        h('div', { class: 'facts' }, facts.map((f) => h('span', { class: 'tag' }, f))),
        media.modules?.length ? h('div', { class: 'small muted', style: { marginBottom: '6px' }, title: t('grab.modulesHint') }, t('grab.foundBy', { list: media.modules.join(', ') })) : null,
        h('a', { class: 'media-link', href: media.page_url, target: '_blank', rel: 'noopener noreferrer', title: media.page_url }, media.page_url))),
    (media.notes || []).map((n) => h('div', { class: 'note' }, n)),
    translationsBlock(),
    episodesBlock()));
}

function translationsBlock() {
  const tr = session.result.media.translations || [];
  if (!tr.length) return null;
  const chips = h('div', { class: 'chips' }, tr.map((c) => h('button', {
    type: 'button', class: 'chip', title: c.label, dataset: { id: c.id }, aria: { pressed: String(session.selection.translation === c.id) },
    onclick: () => {
      if (session.selection.translation === c.id) return;
      for (const b of chips.children) b.setAttribute('aria-pressed', String(b.dataset.id === c.id));
      select({ translation: c.id });
    },
  }, h('span', null, c.label))));
  return h('div', null, h('div', { class: 'subhead' }, h('h3', null, t('grab.translations')), h('span', { class: 'n' }, String(tr.length))), chips);
}

function episodesBlock() {
  const eps = session.result.media.episodes || [];
  if (!eps.length) return null;
  const seasons = [...new Set(eps.map((e) => e.season || ''))];
  if (!seasons.includes(session.season)) session.season = seasons[0];
  const visible = seasons.length > 1 ? eps.filter((e) => (e.season || '') === session.season) : eps;
  const count = h('span', { class: 'n' });

  const grid = h('div', { class: 'episodes', role: 'listbox', dataset: { keep: 'eps' }, aria: { label: t('grab.episodes') } });
  const paint = () => {
    for (const b of grid.children) {
      const id = b.dataset.id;
      b.classList.toggle('is-marked', session.multi && session.marked.has(id));
      b.setAttribute('aria-pressed', String(!session.multi && session.selection.episode === id));
    }
    grid.setAttribute('aria-multiselectable', String(session.multi));
    count.textContent = session.multi ? t('grab.markedCount', { count: session.marked.size }) : String(eps.length);
    renderFoot();
  };
  visible.forEach((e, i) => grid.append(h('button', {
    type: 'button', class: 'ep', title: e.label, dataset: { id: e.id }, role: 'option',
    onclick: () => {
      if (session.multi) {
        session.marked.has(e.id) ? session.marked.delete(e.id) : session.marked.add(e.id);
        paint();
      } else if (session.selection.episode !== e.id) {
        select({ episode: e.id });
        paint();
      }
    },
  }, String(e.number || i + 1))));

  const tools = h('span', { class: 'row' },
    h('button', { type: 'button', class: 'btn btn--ghost btn--sm', onclick: () => { visible.forEach((e) => session.marked.add(e.id)); paint(); } }, t('grab.all')),
    h('button', { type: 'button', class: 'btn btn--ghost btn--sm', onclick: () => { session.marked.clear(); paint(); } }, t('grab.none')));
  tools.hidden = !session.multi;
  const multi = h('label', { class: 'check small' }, h('input', {
    type: 'checkbox', checked: session.multi, onchange: (ev) => {
      session.multi = ev.target.checked;
      if (session.multi && !session.marked.size && session.selection.episode) session.marked.add(session.selection.episode);
      tools.hidden = !session.multi;
      paint();
    },
  }), t('grab.multi'));

  const box = h('div', { class: 'episodes-box' },
    h('div', { class: 'subhead' }, h('h3', null, t('grab.episodes')), count, h('div', { class: 'tools' }, tools, multi)),
    seasons.length > 1 ? h('div', { class: 'chips', style: { marginBottom: '8px' } }, seasons.map((s) => h('button', {
      type: 'button', class: 'chip', aria: { pressed: String(s === session.season) },
      onclick: () => { session.season = s; renderMedia(); },
    }, h('span', null, s || t('grab.noSeason'))))) : null,
    grid);
  paint();
  requestAnimationFrame(() => {
    const sel = grid.querySelector('[aria-pressed="true"]');
    if (sel && grid.scrollTop === 0) sel.scrollIntoView({ block: 'nearest' });
  });
  return box;
}

async function select(patch) {
  const prev = session.selection;
  const next = { ...prev, ...patch };
  session.selection = next;
  setResolving(true);
  try {
    const st = await post(`/api/extract/${session.result.id}/resolve`, next);
    if (session.selection !== next) return; // a newer selection is in flight
    session.stream = st;
    session.out.format = '';
    session.out.audio = '';
    session.out.subtitles = new Set();
    setResolving(false);
    if (ui) { renderSource(); renderOutput(); }
  } catch (err) {
    if (session.selection !== next) return;
    toast(err.message, 'error');
    session.selection = prev;
    setResolving(false);
    if (ui) renderMedia();
  }
}

function setResolving(on) {
  if (!ui?.source) return;
  ui.resolving = on;
  ui.source.n.replaceChildren(on ? h('span', { class: 'spinner', style: { width: '12px', height: '12px', display: 'inline-block', verticalAlign: '-2px' } }) : String(session.stream?.formats?.length || ''));
  ui.source.body.style.opacity = on ? '.5' : '';
  ui.output.body.style.opacity = on ? '.5' : '';
  renderFoot();
}

// ── Source column ──────────────────────────────────────────────────────────

function currentFormat() {
  const st = session.stream;
  return st.formats.find((f) => f.id === session.out.format) || st.formats[0];
}

function formatMeta(f) {
  const parts = [];
  if (f.vcodec) parts.push(f.vcodec);
  if (f.acodec) parts.push(f.acodec);
  if (f.fps && f.fps > 30) parts.push(`${Math.round(f.fps)}fps`);
  if (f.bitrate) parts.push(fmt.bitrate(f.bitrate));
  parts.push(f.protocol === 'ytdlp' ? (f.ext || 'yt-dlp') : f.protocol.toUpperCase());
  return parts.join(' · ');
}

// A list of selectable rows; selecting only toggles aria-pressed in place.
function choiceList(items, selected, onPick) {
  const list = h('div', { class: 'formats', role: 'listbox' });
  for (const it of items) {
    list.append(h('button', {
      type: 'button', class: 'fmt', role: 'option', dataset: { id: it.id }, aria: { pressed: String(it.id === selected) },
      onclick: () => {
        for (const b of list.children) b.setAttribute('aria-pressed', String(b.dataset.id === it.id));
        onPick(it.id);
      },
    }, h('span', { class: 'fmt__q' }, it.q), h('span', { class: 'fmt__meta', title: it.meta }, it.meta), h('span', { class: 'fmt__side' }, it.side)));
  }
  return list;
}

function renderSource() {
  const col = ui.source;
  const st = session.stream;
  const o = session.out;
  col.n.textContent = st?.formats?.length ? String(st.formats.length) : '';
  if (!st?.formats?.length) {
    mount(col.body, h('div', { class: 'state' }, icon('film'), h('p', null, t('grab.noFormats'))));
    return;
  }
  const f = currentFormat();
  o.format = f.id;

  const kind = segmented([
    { value: false, label: t('grab.video'), icon: 'film' },
    { value: true, label: t('grab.audioOnly'), icon: 'music' },
  ], o.audioOnly, (v) => { o.audioOnly = v; renderSource(); renderOutput(); }, { label: t('grab.kind') });

  let list;
  if (o.audioOnly && st.audio?.length) {
    if (!st.audio.some((a) => a.id === o.audio)) o.audio = st.audio[0].id;
    list = [h('div', { class: 'subhead' }, h('h3', null, t('grab.audioTrack')), h('span', { class: 'n' }, String(st.audio.length))), choiceList(st.audio.map((a) => ({
      id: a.id, q: a.bitrate ? `${Math.round(a.bitrate / 1000)}k` : (a.codec || '—'),
      meta: [a.label, a.codec].filter(Boolean).join(' · '), side: a.lang || '',
    })), o.audio, (id) => { o.audio = id; renderFoot(); })];
  } else {
    list = [h('div', { class: 'subhead' }, h('h3', null, o.audioOnly ? t('grab.sourceFile') : t('grab.quality'))), choiceList(st.formats.map((x) => ({
      id: x.id, q: x.height ? `${x.height}p` : x.label, meta: formatMeta(x),
      side: x.size ? fmt.bytes(x.size) : (x.has_audio ? '' : t('grab.videoOnly')),
    })), o.format, (id) => { o.format = id; renderOutput(); })];
  }

  let audioSel = null;
  if (!o.audioOnly && st.audio?.length) {
    const opts = [{ value: '', label: f.has_audio ? t('grab.audioDefault') : t('grab.audioAuto') }];
    for (const a of st.audio) opts.push({ value: a.id, label: [a.label, a.codec, a.bitrate ? fmt.bitrate(a.bitrate) : ''].filter(Boolean).join(' · ') });
    if (!opts.some((x) => x.value === o.audio)) o.audio = '';
    audioSel = field(t('grab.audioTrack'), selectEl(opts, o.audio, (v) => { o.audio = v; renderFoot(); }));
  }

  let subs = null;
  if (!o.audioOnly && st.subtitles?.length) {
    subs = h('div', null, h('div', { class: 'subhead' }, h('h3', null, t('grab.subtitles')), h('span', { class: 'n' }, String(st.subtitles.length))),
      h('div', { class: 'chips' }, st.subtitles.map((s) => h('button', {
        type: 'button', class: 'chip', aria: { pressed: String(o.subtitles.has(s.id)) },
        onclick: (e) => {
          o.subtitles.has(s.id) ? o.subtitles.delete(s.id) : o.subtitles.add(s.id);
          e.currentTarget.setAttribute('aria-pressed', String(o.subtitles.has(s.id)));
          renderFoot();
        },
      }, h('span', null, s.label)))));
  }

  keepScroll(col.body, () => mount(col.body, kind, h('div', null, list), audioSel, subs));
}

// ── Output column ──────────────────────────────────────────────────────────

function containersFor(audioOnly) {
  return (caps().containers || []).filter((c) => c.audio_only === audioOnly);
}

function codecLabel(c, kind) {
  if (c === 'copy') return kind === 'video' ? t('codec.copyVideo') : t('codec.copyAudio');
  return t(`codec.${c}`);
}

function currentContainer() {
  const o = session.out;
  const list = containersFor(o.audioOnly);
  const ct = list.find((c) => c.id === (o.audioOnly ? o.audioContainer : o.container)) || list[0];
  if (ct) { if (o.audioOnly) o.audioContainer = ct.id; else o.container = ct.id; }
  return ct;
}

function renderOutput() {
  const col = ui.output;
  const st = session.stream;
  const o = session.out;
  if (!st?.formats?.length) {
    mount(col.body);
    renderFoot();
    return;
  }
  const ct = currentContainer();
  if (!ct) {
    mount(col.body, h('div', { class: 'state' }, h('p', null, t('grab.noContainers'))));
    renderFoot();
    return;
  }
  col.extra.replaceChildren(h('span', { class: 'tag tag--accent' }, ct.label));
  const f = currentFormat();
  const parts = [];

  parts.push(field(t('grab.container'), h('div', { class: 'chips' }, containersFor(o.audioOnly).map((c) => h('button', {
    type: 'button', class: 'chip', aria: { pressed: String(c.id === ct.id) },
    onclick: () => { if (o.audioOnly) o.audioContainer = c.id; else o.container = c.id; o.videoCodec = ''; o.audioCodec = ''; renderOutput(); },
  }, h('span', null, c.label))))));

  if (!ct.audio_codecs.includes(o.audioCodec)) o.audioCodec = ct.audio_codecs[0];
  const audioCodec = () => field(t('grab.audioCodec'), selectEl(ct.audio_codecs.map((c) => ({ value: c, label: codecLabel(c, 'audio') })), o.audioCodec, (v) => { o.audioCodec = v; renderFoot(); }));
  const audioBitrate = () => field(t('grab.audioBitrate'), selectEl([0, 96, 128, 160, 192, 256, 320].map((b) => ({ value: b, label: b ? `${b} kbps` : t('grab.auto') })), o.audioBitrate, (v) => { o.audioBitrate = Number(v); renderFoot(); }));

  let reencode = false;
  if (!o.audioOnly) {
    if (!ct.video_codecs.includes(o.videoCodec)) o.videoCodec = ct.video_codecs[0];
    const srcH = f.height || Math.max(0, ...st.formats.map((x) => x.height || 0));
    const heights = (caps().heights || []).filter((x) => x === 0 || !srcH || x < srcH);
    if (!heights.includes(o.height)) o.height = 0;
    reencode = o.videoCodec !== 'copy' || o.height > 0;
    parts.push(h('div', { class: 'grid2' },
      field(t('grab.videoCodec'), selectEl(ct.video_codecs.map((c) => ({ value: c, label: codecLabel(c, 'video') })), o.videoCodec, (v) => { o.videoCodec = v; renderOutput(); })),
      field(t('grab.resolution'), selectEl(heights.map((x) => ({ value: x, label: x ? `${x}p` : t('grab.original') })), o.height, (v) => { o.height = Number(v); renderOutput(); }))));
    if (o.videoCodec === 'copy' && o.height) parts.push(h('p', { class: 'hint', style: { marginTop: '-8px' } }, t('grab.copyScaleHint')));
  } else {
    parts.push(h('div', { class: 'grid2' }, audioCodec(), audioBitrate()));
  }

  // Advanced options
  parts.push(h('button', { type: 'button', class: 'adv-toggle', aria: { expanded: String(session.advanced) },
    onclick: () => { session.advanced = !session.advanced; renderOutput(); } }, icon('plus'), t('grab.advanced')));
  if (session.advanced) {
    if (!o.audioOnly) parts.push(h('div', { class: 'grid2' }, audioCodec(), audioBitrate()));
    if (reencode) {
      parts.push(h('div', { class: 'grid2' },
        field(t('grab.crf'), h('input', { class: 'input mono', type: 'number', min: 0, max: 63, placeholder: t('grab.auto'), value: o.crf || '', oninput: (e) => { o.crf = Number(e.target.value) || 0; } })),
        ['h264', 'h265'].includes(o.videoCodec) || (o.videoCodec === 'copy' && o.height)
          ? field(t('grab.preset'), selectEl([{ value: '', label: t('grab.auto') }, ...(caps().presets || []).map((p) => ({ value: p, label: p }))], o.preset, (v) => { o.preset = v; }))
          : h('span')));
    }
    parts.push(h('div', { class: 'grid2' },
      field(t('grab.trimStart'), h('input', { class: 'input mono', placeholder: '0:00', value: o.start, oninput: (e) => { o.start = e.target.value.trim(); renderFoot(); } })),
      field(t('grab.trimEnd'), h('input', { class: 'input mono', placeholder: fmt.duration(st.duration), value: o.end, oninput: (e) => { o.end = e.target.value.trim(); renderFoot(); } }))));
  }

  // Delivery
  const deliveryHint = h('p', { class: 'hint' }, t(o.mode === 'stream' ? 'grab.modeStreamHint' : 'grab.modeFileHint'));
  parts.push(h('div', null, h('span', { class: 'label' }, t('grab.delivery')), segmented([
    { value: 'stream', label: t('grab.modeStream'), icon: 'bolt' },
    { value: 'file', label: t('grab.modeFile'), icon: 'file' },
  ], o.mode, (v) => { o.mode = v; deliveryHint.textContent = t(v === 'stream' ? 'grab.modeStreamHint' : 'grab.modeFileHint'); renderFoot(); }), deliveryHint));

  if (isIndividual() && user()) {
    const opts = [{ value: '', label: t('grab.noCategory') }, ...session.categories.map((c) => ({ value: c.id, label: c.name }))];
    if (!opts.some((x) => String(x.value) === String(o.categoryId))) o.categoryId = '';
    parts.push(field(t('grab.category'), selectEl(opts, o.categoryId, (v) => { o.categoryId = v; })));
  }

  keepScroll(col.body, () => mount(col.body, parts));
  renderFoot();
}

function selectedItems() {
  const media = session.result.media;
  if (session.multi) {
    return media.episodes.filter((e) => session.marked.has(e.id)).map((e) => ({ translation: session.selection.translation, episode: e.id }));
  }
  return [{ ...session.selection }];
}

/** Footer of the output column: one-line summary + Download button. */
function renderFoot() {
  const foot = ui?.output?.foot;
  if (!foot) return;
  if (!session.stream?.formats?.length) { mount(foot); return; }
  const o = session.out;
  const ct = currentContainer();
  if (!ct) { mount(foot); return; }
  const f = currentFormat();
  const items = selectedItems();
  const summary = o.audioOnly
    ? [ct.label, codecLabel(o.audioCodec, 'audio'), o.audioBitrate ? `${o.audioBitrate}k` : '']
    : [ct.label, o.videoCodec === 'copy' && !o.height ? t('grab.sumCopy') : t(`codec.${o.videoCodec === 'copy' ? 'h264' : o.videoCodec}`),
      o.height ? `${o.height}p` : (f.height ? `${f.height}p` : '')];
  summary.push(t(o.mode === 'stream' ? 'grab.modeStream' : 'grab.modeFile'));
  if (o.start || o.end) summary.push(`${o.start || '0:00'}–${o.end || '…'}`);

  const btn = h('button', {
    type: 'button', class: 'btn btn--primary btn--lg', disabled: !!ui.resolving || !items.length,
    onclick: () => startDownload(btn),
  }, icon('grab'), h('span', null, items.length > 1 ? t('grab.downloadN', { count: items.length }) : t('grab.download')));
  mount(foot,
    h('div', { class: 'grow summary' }, h('b', { class: 'ellipsis' }, summary.filter(Boolean).join(' · ')),
      items.length > 1 ? h('span', { class: 'small muted ellipsis' }, t('grab.batchHint')) : null),
    btn);
}

function startDownload(btn) {
  const o = session.out;
  const st = session.stream;
  const f = currentFormat();
  const audioTrack = st.audio?.find((a) => a.id === o.audio);
  const items = selectedItems();
  const body = {
    media_id: session.result.id,
    items,
    pick: {
      format: f.id,
      quality: items.length > 1 ? (f.height || 0) : 0,
      audio: o.audio ? [o.audio] : [],
      audio_lang: items.length > 1 && audioTrack ? (audioTrack.lang || audioTrack.label) : '',
      subtitles: [...o.subtitles],
    },
    output: {
      container: o.audioOnly ? o.audioContainer : o.container,
      video_codec: o.audioOnly ? '' : o.videoCodec,
      audio_codec: o.audioCodec,
      height: o.audioOnly ? 0 : o.height,
      crf: o.crf || 0,
      preset: o.preset,
      audio_bitrate: o.audioBitrate || 0,
      start: o.start,
      end: o.end,
    },
    mode: o.mode,
    category_id: o.categoryId ? Number(o.categoryId) : null,
  };
  // Immediate feedback: the button shows a spinner until the job exists,
  // and the progress dialog opens right away.
  btn.disabled = true;
  btn.replaceChildren(h('span', { class: 'spinner' }), h('span', null, t('grab.preparing')));
  const restore = () => { if (btn.isConnected) renderFoot(); };
  openProgress({
    mode: o.mode,
    navigate: ui.navigate,
    create: () => post('/api/jobs', body).then((res) => {
      restore();
      savePrefs({
        container: o.container, audioContainer: o.audioContainer, videoCodec: o.videoCodec, height: o.height,
        audioBitrate: o.audioBitrate, mode: o.mode, categoryId: o.categoryId,
      });
      return res.jobs;
    }, (err) => { restore(); throw err; }),
  });
}
