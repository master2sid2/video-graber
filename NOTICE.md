# Third-party notices

## External programs (not bundled, not linked)

VideoGraber runs the following programs as separate processes. They are not
distributed with VideoGraber and no part of their source code is included.

* **yt-dlp** — https://github.com/yt-dlp/yt-dlp — The Unlicense.
  Used for YouTube and other supported sites. The list of hosts delegated to
  yt-dlp and the handling of its JSON output (`-J`) follow its documented
  command line interface.
* **FFmpeg / ffprobe** — https://ffmpeg.org — LGPL 2.1+ / GPL 2+ depending on
  the build. Used for downloading streams, muxing, conversion and probing.
* **Chrome / Chromium** (optional) — driven through the DevTools protocol for
  headless extraction.

## Go modules

| Module | License |
| --- | --- |
| github.com/chromedp/chromedp, github.com/chromedp/cdproto | MIT |
| golang.org/x/net, golang.org/x/crypto, golang.org/x/sys | BSD-3-Clause |
| modernc.org/sqlite and its modernc.org dependencies | BSD-3-Clause |
| github.com/dustin/go-humanize | MIT |
| github.com/google/uuid | BSD-3-Clause |
| github.com/mattn/go-isatty | MIT |
| github.com/ncruces/go-strftime | MIT |
| github.com/remyoudompheng/bigfft | BSD-3-Clause |
| github.com/go-json-experiment/json | BSD-3-Clause |

## Fonts (web/fonts)

Self-hosted subsets (Latin, Latin Extended, Cyrillic, Cyrillic Extended),
all under the SIL Open Font License 1.1:

* **Unbounded** — © The Unbounded Project Authors
* **Golos Text** — © The Golos Project Authors (Paratype)
* **JetBrains Mono** — © The JetBrains Mono Project Authors
