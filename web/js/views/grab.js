// Grab view: paste a URL, pick episode / voice-over / quality, download.

import { t } from '../i18n.js';
import { h, icon, mount, toast, thumb, fmt, segmented, selectEl, field } from '../dom.js';
import { get, post } from '../api.js';
import { state, caps, isIndividual, user, deliver, savePrefs } from '../state.js';

// The grab state survives navigation between views within the session.
const session = {
  url: '',
  extractor: 'auto',
  result: null,     // {id, media, stream}
  stream: null,
  selection: { translation: '', episode: '' },
  marked: new Set(),
  multi: false,
  season: '',
  out: null,        // output settings
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

export async function render(root, { query }) {
  session.out ||= defaults();
  const resultBox = h('div', { class: 'result' });
  const slateEl = slate(resultBox);
  mount(root,
    h('div', { class: 'page-head' }, h('h1', { class: 'page-title' }, h('small', null, t('grab.eyebrow')), t('grab.title'))),
    slateEl,
    resultBox);

  if (isIndividual() && user()) {
    get('/api/categories').then((c) => {
      session.categories = c || [];
      const panel = root.querySelector('.outpanel');
      if (session.result && panel) renderOutput(panel);
    }).catch(() => {});
  }

  const qURL = query.get('url');
  if (qURL && qURL !== session.url) {
    session.url = qURL;
    slateEl.querySelector('.slate__url').value = qURL;
    if (query.get('extractor')) session.extractor = query.get('extractor');
    history.replaceState(null, '', '#/grab');
    runGrab(slateEl, resultBox);
  } else if (session.result) {
    renderResult(resultBox);
  } else {
    renderIntro(resultBox);
  }
}

// ── Slate ──────────────────────────────────────────────────────────────────

function slate(resultBox) {
  const st = state.status;
  const extractors = (st.extractors || ['auto']).map((e) => ({ value: e, label: t(`extractor.${e}`) }));
  const input = h('input', {
    class: 'slate__url', type: 'url', inputmode: 'url', autocomplete: 'off', spellcheck: 'false', required: true,
    placeholder: t('grab.placeholder'), value: session.url, aria: { label: t('grab.urlLabel') },
  });
  const btn = h('button', { class: 'btn btn--rec btn--lg', type: 'submit' }, icon('bolt'), t('grab.go'));
  const form = h('form', { class: 'slate', onsubmit: (e) => { e.preventDefault(); session.url = input.value.trim(); runGrab(form, resultBox); } },
    h('div', { class: 'slate__clapper', aria: { hidden: 'true' } }),
    h('div', { class: 'slate__stripe', aria: { hidden: 'true' } }),
    h('div', { class: 'slate__body' },
      h('label', { class: 'slate__cell' }, h('span', { class: 'label' }, t('grab.scene')), input),
      h('label', { class: 'slate__cell slate__take' }, h('span', { class: 'label' }, t('grab.take')),
        selectEl(extractors, session.extractor, (v) => { session.extractor = v; })),
      h('div', { class: 'slate__cell slate__go' }, btn)),
    h('div', { class: 'slate__meta' },
      h('span', null, t('grab.metaMode'), ' ', h('b', null, t(`mode.${st.mode}`))),
      h('span', null, 'yt-dlp ', h('b', { class: st.features.ytdlp ? 'ok' : '' }, st.features.ytdlp ? t('common.on') : t('common.off'))),
      h('span', null, t('grab.metaBrowser'), ' ', h('b', { class: st.features.browser ? 'ok' : '' }, st.features.browser ? t('common.on') : t('common.off'))),
      h('span', null, t('grab.metaWorkers'), ' ', h('b', null, String(st.max_jobs)))));
  form.addEventListener('paste', () => setTimeout(() => { if (input.value.trim() && !session.result) input.focus(); }, 0));
  return form;
}

function renderIntro(box) {
  mount(box, h('div', { class: 'cols stagger' },
    introCard('film', t('grab.intro1Title'), t('grab.intro1Text')),
    introCard('queue', t('grab.intro2Title'), t('grab.intro2Text')),
    introCard('music', t('grab.intro3Title'), t('grab.intro3Text'))));
}

function introCard(ic, title, text) {
  return h('div', { class: 'panel' }, h('div', { class: 'panel__body' },
    h('div', { class: 'row' }, icon(ic), h('h3', { class: 'panel__title' }, title)),
    h('p', { class: 'muted', style: { margin: '12px 0 0' } }, text)));
}

async function runGrab(form, box) {
  if (!session.url) return;
  form.classList.remove('is-clapping');
  void form.offsetWidth;
  form.classList.add('is-clapping');
  const btn = form.querySelector('[type=submit]');
  btn.disabled = true;
  mount(box, h('div', { class: 'scanning' },
    h('div', { class: 'scanning__beam' }),
    h('div', null, t('grab.scanning')),
    h('div', { class: 'mono' }, t('grab.scanningHint'))));
  try {
    const res = await post('/api/extract', { url: session.url, extractor: session.extractor === 'auto' ? '' : session.extractor });
    session.result = res;
    session.stream = res.stream;
    session.selection = { ...res.media.selected };
    session.marked = new Set();
    session.multi = false;
    session.season = seasonOf(res.media, res.media.selected.episode);
    session.out = { ...defaults(), categoryId: session.out?.categoryId || '' };
    renderResult(box);
  } catch (err) {
    mount(box, h('div', { class: 'error-card' }, h('h3', null, t('grab.failed')), h('p', null, err.message),
      state.status.features.browser || session.extractor !== 'auto' ? null : h('p', { class: 'small', style: { marginTop: '8px' } }, t('grab.failedHint'))));
  } finally {
    btn.disabled = false;
  }
}

function seasonOf(media, epID) {
  return media.episodes?.find((e) => e.id === epID)?.season || media.episodes?.[0]?.season || '';
}

// ── Result ─────────────────────────────────────────────────────────────────

function renderResult(box) {
  const output = h('aside', { class: 'panel outpanel' });
  const left = h('section', { class: 'panel mediacard' });
  mount(box, h('div', { class: 'grab stagger' }, left, output));
  renderMedia(left, output);
  renderOutput(output);
}

function renderMedia(left, output) {
  const { media } = session.result;
  const st = session.stream;
  const facts = [
    [t('grab.factSource'), media.extractor],
    st?.duration || media.duration ? [t('grab.factDuration'), fmt.duration(st?.duration || media.duration)] : null,
    [t('grab.factFormats'), String(st?.formats?.length || 0)],
    media.episodes?.length ? [t('grab.factEpisodes'), String(media.episodes.length)] : null,
    media.translations?.length ? [t('grab.factTranslations'), String(media.translations.length)] : null,
    st?.audio?.length ? [t('grab.factAudio'), String(st.audio.length)] : null,
  ].filter(Boolean);

  const wide = !media.episodes?.length;
  mount(left,
    h('div', { class: 'mediacard__hero' },
      h('div', { class: ['poster', wide && 'poster--wide'] }, thumb(media.thumbnail, media.title)),
      h('div', null,
        h('span', { class: 'eyebrow' }, t('grab.found')),
        h('h2', { class: 'media-title' }, media.title || t('grab.untitled')),
        h('ul', { class: 'facts' }, facts.map(([k, v]) => h('li', { class: 'fact' }, k, h('b', null, v)))),
        h('a', { class: 'media-url', href: media.page_url, target: '_blank', rel: 'noopener noreferrer' }, media.page_url),
        (media.notes || []).map((n) => h('div', { class: 'note' }, n)))),
    translationsSection(left, output),
    episodesSection(left, output));
}

function translationsSection(left, output) {
  const tr = session.result.media.translations || [];
  if (!tr.length) return null;
  return h('div', { class: 'section' },
    h('div', { class: 'section__head' }, h('h3', { class: 'section__title' }, t('grab.translations')), h('span', { class: 'count' }, String(tr.length))),
    h('div', { class: 'chips' }, tr.map((c) => h('button', {
      type: 'button', class: 'chip', aria: { pressed: String(session.selection.translation === c.id) },
      onclick: () => select({ translation: c.id }, left, output),
    }, c.label))));
}

function episodesSection(left, output) {
  const eps = session.result.media.episodes || [];
  if (!eps.length) return null;
  const seasons = [...new Set(eps.map((e) => e.season || ''))];
  const visible = seasons.length > 1 ? eps.filter((e) => (e.season || '') === session.season) : eps;

  const strip = h('div', { class: 'filmstrip', role: 'listbox', aria: { label: t('grab.episodes'), multiselectable: String(session.multi) } },
    visible.map((e, i) => {
      const n = e.number || i + 1;
      const pressed = !session.multi && session.selection.episode === e.id;
      return h('button', {
        type: 'button', class: ['frame', session.marked.has(e.id) && 'is-marked'], title: e.label,
        role: 'option', aria: { pressed: String(pressed), selected: String(session.multi ? session.marked.has(e.id) : pressed) },
        onclick: () => {
          if (session.multi) {
            session.marked.has(e.id) ? session.marked.delete(e.id) : session.marked.add(e.id);
            renderMedia(left, output);
            renderOutput(output);
          } else {
            select({ episode: e.id }, left, output);
          }
        },
      }, h('span', { class: 'frame__num' }, String(n)), h('span', { class: 'frame__lab' }, t('grab.ep')));
    }));

  // Keep the selected frame in view.
  requestAnimationFrame(() => strip.querySelector('[aria-pressed="true"]')?.scrollIntoView({ block: 'nearest', inline: 'center' }));

  const tools = h('div', { class: 'strip-tools' },
    h('label', { class: 'check small' }, h('span', { class: 'toggle' },
      h('input', { type: 'checkbox', checked: session.multi, onchange: (ev) => {
        session.multi = ev.target.checked;
        if (session.multi && !session.marked.size && session.selection.episode) session.marked.add(session.selection.episode);
        renderMedia(left, output);
        renderOutput(output);
      } }), h('span')), t('grab.multi')),
    session.multi ? [
      h('button', { type: 'button', class: 'btn btn--ghost btn--sm', onclick: () => { visible.forEach((e) => session.marked.add(e.id)); renderMedia(left, output); renderOutput(output); } }, t('grab.all')),
      h('button', { type: 'button', class: 'btn btn--ghost btn--sm', onclick: () => { session.marked.clear(); renderMedia(left, output); renderOutput(output); } }, t('grab.none')),
    ] : null);

  return h('div', { class: 'section' },
    h('div', { class: 'section__head' },
      h('h3', { class: 'section__title' }, t('grab.episodes')),
      h('span', { class: 'count' }, session.multi ? t('grab.markedCount', { count: session.marked.size }) : String(eps.length)),
      tools),
    seasons.length > 1 ? h('div', { class: 'chips', style: { marginBottom: '12px' } }, seasons.map((s) => h('button', {
      type: 'button', class: 'chip', aria: { pressed: String(s === session.season) },
      onclick: () => { session.season = s; renderMedia(left, output); },
    }, s || t('grab.noSeason')))) : null,
    h('div', { class: 'filmstrip-wrap' }, strip));
}

async function select(patch, left, output) {
  const prev = session.selection;
  const next = { ...session.selection, ...patch };
  session.selection = next;
  renderMedia(left, output);
  mount(output, h('div', { class: 'panel__body' }, h('div', { class: 'row muted' }, h('span', { class: 'spinner' }), t('grab.resolving'))));
  try {
    session.stream = await post(`/api/extract/${session.result.id}/resolve`, next);
    session.out.format = '';
    session.out.audio = '';
    session.out.subtitles = new Set();
  } catch (err) {
    toast(err.message, 'error');
    // Keep the selection consistent with the stream that is still shown.
    if (session.selection === next) session.selection = prev;
  }
  if (session.selection !== next && session.selection !== prev) return; // a newer selection won
  renderMedia(left, output);
  renderOutput(output);
}

// ── Output panel ───────────────────────────────────────────────────────────

function containersFor(audioOnly) {
  return (caps().containers || []).filter((c) => c.audio_only === audioOnly);
}

function codecLabel(c, kind) {
  if (c === 'copy') return kind === 'video' ? t('codec.copyVideo') : t('codec.copyAudio');
  return t(`codec.${c}`);
}

function renderOutput(panel) {
  const st = session.stream;
  const o = session.out;
  if (!st?.formats?.length) {
    mount(panel, h('div', { class: 'panel__body' }, h('p', { class: 'muted' }, t('grab.noFormats'))));
    return;
  }
  const rerender = () => renderOutput(panel);
  const ctID = o.audioOnly ? o.audioContainer : o.container;
  const containers = containersFor(o.audioOnly);
  const ct = containers.find((c) => c.id === ctID) || containers[0];
  if (!ct) {
    mount(panel, h('div', { class: 'panel__body' }, h('p', { class: 'muted' }, t('grab.noContainers'))));
    return;
  }
  if (o.audioOnly) o.audioContainer = ct.id; else o.container = ct.id;

  const fmtSel = st.formats.find((f) => f.id === o.format) || st.formats[0];
  o.format = fmtSel.id;

  const body = h('div', { class: 'panel__body' });

  body.append(segmented([
    { value: false, label: t('grab.video'), icon: 'film' },
    { value: true, label: t('grab.audioOnly'), icon: 'music' },
  ], o.audioOnly, (v) => { o.audioOnly = v; rerender(); }, { block: true, label: t('grab.kind') }));

  // Source: video formats or audio tracks
  if (!o.audioOnly || !st.audio?.length) {
    body.append(field(o.audioOnly ? t('grab.source') : t('grab.quality'), h('div', { class: 'formats' },
      st.formats.map((f) => h('button', {
        type: 'button', class: 'fmt', aria: { pressed: String(f.id === o.format) },
        onclick: () => { o.format = f.id; rerender(); },
      },
      h('span', { class: 'fmt__q' }, f.height ? `${f.height}p` : f.label),
      h('span', { class: 'fmt__meta' }, formatMeta(f)),
      h('span', { class: 'fmt__size' }, f.size ? fmt.bytes(f.size) : (f.has_audio ? '' : h('span', { class: 'tag' }, t('grab.videoOnly')))))))));
  }

  // Audio track
  if (st.audio?.length) {
    const opts = [];
    if (!o.audioOnly) opts.push({ value: '', label: fmtSel.has_audio ? t('grab.audioDefault') : t('grab.audioAuto') });
    for (const a of st.audio) opts.push({ value: a.id, label: [a.label, a.codec, a.bitrate ? fmt.bitrate(a.bitrate) : ''].filter(Boolean).join(' · ') });
    if (o.audioOnly && !o.audio) o.audio = st.audio[0].id;
    body.append(field(t('grab.audioTrack'), selectEl(opts, o.audio, (v) => { o.audio = v; })));
  }

  // Subtitles
  if (!o.audioOnly && ct.subtitles && st.subtitles?.length) {
    body.append(field(t('grab.subtitles'), h('div', { class: 'chips' }, st.subtitles.map((s) => h('button', {
      type: 'button', class: 'chip', aria: { pressed: String(o.subtitles.has(s.id)) },
      onclick: (e) => {
        o.subtitles.has(s.id) ? o.subtitles.delete(s.id) : o.subtitles.add(s.id);
        e.currentTarget.setAttribute('aria-pressed', String(o.subtitles.has(s.id)));
      },
    }, s.label)))));
  }

  // Container
  body.append(field(t('grab.container'), h('div', { class: 'chips' }, containers.map((c) => h('button', {
    type: 'button', class: 'chip', aria: { pressed: String(c.id === ct.id) },
    onclick: () => { if (o.audioOnly) o.audioContainer = c.id; else o.container = c.id; o.videoCodec = ''; o.audioCodec = ''; rerender(); },
  }, c.label)))));

  // Codecs & resolution
  if (!o.audioOnly) {
    if (!ct.video_codecs.includes(o.videoCodec)) o.videoCodec = ct.video_codecs[0];
    const maxH = Math.max(0, ...st.formats.map((f) => f.height || 0));
    const heights = (caps().heights || []).filter((x) => x === 0 || !maxH || x < maxH);
    if (!heights.includes(o.height)) o.height = 0;
    body.append(h('div', { class: 'grid-2' },
      field(t('grab.videoCodec'), selectEl(ct.video_codecs.map((c) => ({ value: c, label: codecLabel(c, 'video') })), o.videoCodec, (v) => { o.videoCodec = v; rerender(); })),
      field(t('grab.resolution'), selectEl(heights.map((x) => ({ value: x, label: x ? `${x}p` : t('grab.original') })), o.height, (v) => { o.height = Number(v); rerender(); }))));
    if (o.videoCodec === 'copy' && o.height) body.append(h('p', { class: 'hint' }, t('grab.copyScaleHint')));
  }
  if (!ct.audio_codecs.includes(o.audioCodec)) o.audioCodec = ct.audio_codecs[0];

  // Advanced
  const reencode = !o.audioOnly && (o.videoCodec !== 'copy' || o.height);
  body.append(h('details', { class: 'adv' },
    h('summary', null, t('grab.advanced')),
    h('div', { class: 'adv__body' },
      h('div', { class: 'grid-2' },
        field(t('grab.audioCodec'), selectEl(ct.audio_codecs.map((c) => ({ value: c, label: codecLabel(c, 'audio') })), o.audioCodec, (v) => { o.audioCodec = v; })),
        field(t('grab.audioBitrate'), selectEl([0, 96, 128, 160, 192, 256, 320].map((b) => ({ value: b, label: b ? `${b} kbps` : t('grab.auto') })), o.audioBitrate, (v) => { o.audioBitrate = Number(v); }))),
      reencode ? h('div', { class: 'grid-2' },
        field(t('grab.crf'), h('input', { class: 'input mono', type: 'number', min: 0, max: 63, placeholder: t('grab.auto'), value: o.crf || '', oninput: (e) => { o.crf = Number(e.target.value) || 0; } }), t('grab.crfHint')),
        ['h264', 'h265'].includes(o.videoCodec) || (o.videoCodec === 'copy' && o.height)
          ? field(t('grab.preset'), selectEl([{ value: '', label: t('grab.auto') }, ...(caps().presets || []).map((p) => ({ value: p, label: p }))], o.preset, (v) => { o.preset = v; }))
          : h('span')) : null,
      h('div', { class: 'grid-2' },
        field(t('grab.trimStart'), h('input', { class: 'input mono', placeholder: '0:00', value: o.start, oninput: (e) => { o.start = e.target.value.trim(); } })),
        field(t('grab.trimEnd'), h('input', { class: 'input mono', placeholder: fmt.duration(st.duration), value: o.end, oninput: (e) => { o.end = e.target.value.trim(); } }))))));

  // Delivery
  body.append(field(t('grab.delivery'), segmented([
    { value: 'stream', label: t('grab.modeStream'), icon: 'bolt' },
    { value: 'file', label: t('grab.modeFile'), icon: 'file' },
  ], o.mode, (v) => { o.mode = v; hint.textContent = t(v === 'stream' ? 'grab.modeStreamHint' : 'grab.modeFileHint'); }, { block: true })));
  const hint = h('p', { class: 'hint', style: { marginTop: '-10px' } }, t(o.mode === 'stream' ? 'grab.modeStreamHint' : 'grab.modeFileHint'));
  body.append(hint);

  // Category
  if (isIndividual() && user()) {
    const opts = [{ value: '', label: t('grab.noCategory') }, ...session.categories.map((c) => ({ value: c.id, label: c.name }))];
    body.append(field(t('grab.category'), selectEl(opts, o.categoryId, (v) => { o.categoryId = v; })));
  }

  // Go
  const items = selectedItems();
  const goBtn = h('button', { type: 'button', class: 'btn btn--rec btn--lg btn--block', onclick: () => startDownload(goBtn) },
    icon('grab'), items.length > 1 ? t('grab.downloadN', { count: items.length }) : t('grab.download'));
  body.append(h('div', { class: 'go-bar' }, goBtn,
    items.length > 1 ? h('p', { class: 'hint' }, t('grab.batchHint')) : null));

  mount(panel,
    h('div', { class: 'panel__head' }, h('h2', { class: 'panel__title' }, t('grab.output')), h('span', { class: 'tag tag--rec' }, ct.label)),
    body);
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

function selectedItems() {
  const media = session.result.media;
  if (session.multi && session.marked.size) {
    return media.episodes.filter((e) => session.marked.has(e.id)).map((e) => ({ translation: session.selection.translation, episode: e.id }));
  }
  return [{ ...session.selection }];
}

async function startDownload(btn) {
  const o = session.out;
  const st = session.stream;
  const f = st.formats.find((x) => x.id === o.format) || st.formats[0];
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
  btn.disabled = true;
  try {
    const res = await post('/api/jobs', body);
    deliver(res.jobs);
    toast(res.jobs.length > 1 ? t('grab.queuedN', { count: res.jobs.length }) : (o.mode === 'stream' ? t('grab.streaming') : t('grab.queued')), 'ok');
    savePrefs({
      container: o.container, audioContainer: o.audioContainer, videoCodec: o.videoCodec, height: o.height,
      audioBitrate: o.audioBitrate, mode: o.mode, categoryId: o.categoryId,
    });
  } catch (err) {
    toast(err.message, 'error', 6000);
  } finally {
    btn.disabled = false;
  }
}

