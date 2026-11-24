# VideoGraber

A self-hosted web application that finds videos on web pages — dorama / anime
sites, embedded players, YouTube and hundreds of other sites — and downloads
them, optionally converting them on the fly with ffmpeg.

* **Automatic source detection** – `<video>` tags, OpenGraph/JSON-LD metadata,
  HLS/DASH/MP4 URLs in scripts and attributes, Playerjs configs, nested player
  iframes (followed up to 3 levels, with the right `Referer`).
* **Episodes and voice-overs** – episode link lists, voice-over / dub tabs
  (`data-url-player`, `data-player`, …), Playerjs playlists with
  translations → seasons → episodes, HLS audio renditions and multiple audio
  streams inside MKV/MP4 files (via ffprobe).
* **Conversion** – MP4, MKV, WebM, MOV, MPEG-TS or audio only (MP3, M4A, Opus,
  OGG, FLAC, WAV); H.264 / H.265 / VP9 / AV1, downscaling, CRF, presets, audio
  bitrate, trimming, subtitles.
* **No temp files by default** – ffmpeg output is piped straight into the HTTP
  response (fragmented MP4 for streaming). Optionally a temp file is prepared
  (exact size, resumable) and deleted right after it has been downloaded or
  after `-temp-ttl`.
* **Batch downloads** – select several episodes in the film strip; the closest
  quality and the same voice-over are used for each.
* **YouTube** through the external [yt-dlp](https://github.com/yt-dlp/yt-dlp)
  binary, with personal `cookies.txt` per user for servers on cloud IPs.
* **Two modes** – *shared* (one interface for everybody, no login) and
  *individual* (accounts, history, categories, personal cookies, invite links).
* **Separate app and access logs**, each to stdout/stderr/any number of files,
  text or JSON, reopened on `SIGHUP` for logrotate.
* Plain HTML/CSS/ES-module UI without a build step, embedded into the binary.

## Requirements

| Tool | Required | Purpose |
| --- | --- | --- |
| Go 1.26+ | to build | – |
| [ffmpeg](https://ffmpeg.org/) + ffprobe | **yes** | downloading HLS/DASH, muxing, conversion, probing |
| [yt-dlp](https://github.com/yt-dlp/yt-dlp) | optional | YouTube and other sites it supports; fallback extractor |
| Chrome / Chromium | optional | headless extraction of JavaScript-only players |

None of these tools is bundled with or linked into VideoGraber: install them
yourself and pass their paths (or keep them in `PATH`).

> Recent yt-dlp versions need a JavaScript runtime such as
> [Deno](https://deno.com/) for YouTube. See the yt-dlp documentation; extra
> yt-dlp options can be passed with `-ytdlp-args`.

## Build and run

```sh
go build -o videograber ./cmd/videograber

# shared mode: one interface for everybody
./videograber -listen :8080 -ytdlp /usr/local/bin/yt-dlp

# individual mode: accounts, history and categories
./videograber -mode individual -data-dir /var/lib/videograber
```

Open `http://localhost:8080`.

The version is shown on the sign-in page and in Settings → About. Release
builds set it with `go build -ldflags "-X main.version=1.2.3" ./cmd/videograber`;
other builds report `dev-<git commit>` (`-dirty` when built with uncommitted
changes).

### First run in individual mode

When no account exists, a one-time **setup token** is written to the
application log:

```
level=WARN msg="INITIAL SETUP REQUIRED: open the web UI and enter this setup token"
level=WARN msg="setup token" token=<random one-time token>
```

The web UI shows the setup page; enter the token and create the
administrator. The token changes on every restart until setup is complete.
Administrators can then create users directly or hand out invite links
(`Admin → Invite links`) for self-registration.

### Debugging an extraction

```sh
./videograber extract -ytdlp yt-dlp https://example.com/show/episode-1
```

prints the detected media, episodes, voice-overs and formats as JSON.

## Configuration

Every flag can also be set with an environment variable: `VG_` + the flag
name in upper case with dashes replaced by underscores (`-max-jobs` →
`VG_MAX_JOBS`). Flags win over the environment.

| Flag | Default | Description |
| --- | --- | --- |
| `-listen` | `:8080` | address to listen on |
| `-tls-cert`, `-tls-key` | – | serve HTTPS |
| `-mode` | `shared` | `shared` or `individual` |
| `-data-dir` | `./data` | SQLite database location |
| `-temp-dir` | system temp | temp files and yt-dlp cookie copies |
| `-web-dir` | – | serve the UI from this directory instead of the embedded copy |
| `-ffmpeg` / `-ffprobe` | `ffmpeg` / next to ffmpeg | tool paths |
| `-ytdlp` | look up `yt-dlp` in PATH | path, or `off` |
| `-ytdlp-args` | – | extra arguments for every yt-dlp call |
| `-ytdlp-hosts` | – | extra comma separated hosts always handled by yt-dlp |
| `-chrome` | disabled | Chrome/Chromium path or `auto` |
| `-browser-timeout` | `30s` | how long the headless browser waits for media |
| `-proxy` | – | proxy for page requests, yt-dlp, ffmpeg and Chrome |
| `-report-url` | GitHub issue | link shown for unsupported sites (`https://…` or `mailto:…`; `{url}`, `{title}`, `{body}` are replaced; `off` hides it). Default: a prefilled issue in [master2sid2/video-graber](https://github.com/master2sid2/video-graber/issues) |
| `-user-agent` | Chrome UA | User-Agent of outgoing requests |
| `-max-jobs` | `2` | concurrent ffmpeg jobs |
| `-temp-ttl` | `1h` | delete prepared files that were not downloaded |
| `-job-retention` | `24h` | keep finished jobs in the queue |
| `-session-ttl` | `720h` | login session lifetime |
| `-extract-timeout` | `90s` | limit for one page analysis |
| `-secure-cookies` | `false` | force `Secure` cookies (automatic over TLS) |
| `-trust-proxy` | `false` | trust `X-Forwarded-For` / `X-Forwarded-Proto` |
| `-allow-private` | `false` | allow extraction from private network addresses |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-log-format` | `text` | `text` or `json` |
| `-log-output` | `stdout` | app log outputs, comma separated |
| `-access-log-output` | `stdout` | access log outputs, comma separated (`none` disables) |
| `-access-log-format` | `combined` | `combined` (Apache) or `json` |

### Logging examples

```sh
# app log to a file only, access log to stdout and a file
./videograber -log-output /var/log/vg/app.log -access-log-output stdout,/var/log/vg/access.log

# JSON everywhere, no access log
./videograber -log-format json -access-log-output none
```

Send `SIGHUP` after rotating to reopen all log files.

### YouTube on cloud servers

YouTube frequently refuses requests from datacenter IPs. Cookies of a
signed-in session (exported from a private window in the Netscape
`cookies.txt` format) help.

Cookies are **strictly personal**: in individual mode every user uploads
their own under **Settings → YouTube cookies**, and they are used only for
that user's downloads. There are no global or administrator cookies and no
server-wide cookies file, so nobody ever downloads with somebody else's
account. Shared mode has no accounts and therefore no cookies at all.
(Instance-wide cookies stored by older versions are deleted at startup.)

Each yt-dlp call works on a private temporary copy of the user's cookies,
because yt-dlp rewrites cookie files. Cookie values are never sent back to
the browser — the UI only shows how many cookies and domains are stored.

## Security notes

* Page fetches refuse loopback/private/link-local destinations unless
  `-allow-private` is set (checked at connect time, so DNS rebinding does not
  help). URLs handed to ffmpeg are checked too, and ffmpeg is restricted to
  `http,https,tcp,tls,crypto,data` protocols.
* Passwords are hashed with argon2id; session tokens are stored hashed.
  Login, setup and registration are rate limited per IP.
* Cross-origin state-changing requests are rejected (Go's
  `http.CrossOriginProtection`); the UI is served with a strict CSP and never
  inserts scraped data as HTML.
* Put VideoGraber behind a TLS-terminating reverse proxy (and use
  `-trust-proxy`) when it is reachable from the internet.

## Project layout

```
cmd/videograber     entry point
internal/config     flags + VG_* environment
internal/logging    app/access log sinks (stdout, files, SIGHUP reopen)
internal/netx       outgoing HTTP client with SSRF guard
internal/extract    extractors: generic page analysis, HLS, Playerjs,
                    yt-dlp, headless Chrome, ffprobe
internal/media      ffmpeg planning and execution (pipes, progress)
internal/jobs       job queue, streaming/temp-file delivery, SSE events
internal/store      SQLite (users, sessions, invites, history, categories)
internal/server     HTTP API and static UI
web/                UI (HTML, CSS, ES modules, fonts), embedded with go:embed
```

### Adding a UI language

Copy `web/js/locales/en.js` to `web/js/locales/<code>.js`, translate the
values and register it in `web/js/i18n.js`. A language selector appears in
Settings as soon as more than one language is available.

### How pages are analysed

Every URL goes through the same pipeline:

1. **Site-specific extractors** (`extract.Extractor`) for hosts with an API
   or special handling; yt-dlp is one of them for YouTube & co.
2. **The generic extractor**, which runs every **analysis module** on the page
   and on each embedded player it follows (iframes, also ones injected by
   scripts; the player with the longest episode list wins):

   | Module | Recognises |
   | --- | --- |
   | `html5-video` | `<video>`, `<source>`, `<track>` |
   | `json-ld` | schema.org `VideoObject.contentUrl` |
   | `iframes` | embedded players (followed up to 3 levels) |
   | `player-tabs` | voice-over / server tabs (`data-url-player`, `data-player`, …) |
   | `episode-links` | links to episode pages ("Episode 5", "5 серия"; downloads/torrents are ignored) |
   | `playerjs` | Playerjs `file:` configs and playlists |
   | `js-playlist` | JavaScript arrays like `[{title: 'Episode 1', file: '…'}]`, nested quality lists (`files`, `sources`) |
   | `media-urls` | bare `.m3u8` / `.mpd` / `.mp4` URLs in the document |

   Pages in legacy encodings (windows-1251, koi8-r, …) are converted to
   UTF-8 first. Playlists served by scripts without an extension are sniffed.
3. **The headless browser** (`-chrome`) when static analysis found nothing.
4. **yt-dlp** as the last fallback.

If nothing matches, the user sees "This site is not supported yet" with the
list of tried modules and a "Report on GitHub" button that opens a new issue
in [master2sid2/video-graber](https://github.com/master2sid2/video-graber/issues)
prefilled with the page URL, the tried modules, the version and the error
details (configurable with `-report-url`).
The detected modules are shown under the title of every grabbed page.

### Adding an analysis module

Add a `moduleFunc{name, fn}` to `extract.Modules` (`internal/extract/modules.go`).
A module receives the parsed document (`doc`: DOM, base URL, unescaped raw
text) and contributes findings to the `page`: candidates, a playlist,
episode links, player tabs or iframes. Keep modules generic — one page
*pattern*, not one site.

### Adding a site-specific extractor

Implement `extract.Extractor` (`Name`, `Match`, `Extract`) and register it in
`extract.NewEngine` (`e.Specific`). Specific extractors are also used for
embedded player iframes found by the generic extractor.

## Third-party software

See [NOTICE.md](NOTICE.md). VideoGraber does not include code from yt-dlp or
ffmpeg; it runs them as external programs.

## Legal

Download only content you have the right to download. Respect the terms of
service of the sites you use and the copyright of the content owners.
