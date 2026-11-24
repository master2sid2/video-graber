# Changelog

All notable changes to this project are documented here. Versions follow
[semantic versioning](https://semver.org).

## [v0.1.0] - 2026-11-25

First release.

- Video detection on web pages: `<video>` tags, OpenGraph/JSON-LD, HLS/DASH/MP4 URLs in scripts
  and attributes, Playerjs configs, nested player iframes; pluggable page analysis modules and
  legacy charsets.
- Episodes and voice-overs: episode link lists, dub tabs, Playerjs playlists, HLS audio
  renditions and multiple audio streams (via ffprobe); batch downloads of several episodes.
- Downloads through ffmpeg with optional conversion (MP4, MKV, WebM, MOV, MPEG-TS, audio only;
  H.264/H.265/VP9/AV1, scaling, CRF, trimming, subtitles), streamed without temp files by
  default.
- YouTube and other sites through an external yt-dlp, with personal cookies per user.
- Shared mode (no login) and individual mode (accounts, history, categories, invite links); a
  persistent job queue.
- Separate application and access logs, reopened on SIGHUP.
- Web UI embedded into a single static binary; the version is shown on the sign-in pages.
