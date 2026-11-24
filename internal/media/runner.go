package media

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/master2sid2/video-graber/internal/extract"
)

// Progress is a snapshot reported while ffmpeg runs.
type Progress struct {
	OutTime float64 `json:"out_time"` // seconds of media produced
	Size    int64   `json:"size"`     // bytes written
	Speed   float64 `json:"speed"`    // x realtime
	Percent float64 `json:"percent"`  // 0..100, 0 when duration is unknown
}

// Runner executes plans.
type Runner struct {
	FFmpeg    string
	YtDlp     *extract.YtDlp
	Proxy     string
	UserAgent string
	Log       *slog.Logger
	// HLSExtensionPicky is set when ffmpeg's HLS demuxer has -extension_picky (ffmpeg 7+).
	HLSExtensionPicky bool
}

// tailBuffer keeps the last n bytes written to it (for error messages).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	n   int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// Args builds the ffmpeg command line. inputFDs maps yt-dlp inputs to the
// file descriptors where their output will be readable; progressFD is the
// descriptor for -progress. dst is "pipe:1" or a file path.
func (r *Runner) Args(p *Plan, inputFDs map[int]int, progressFD int, dst string, streaming bool) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-y",
		"-progress", "pipe:" + strconv.Itoa(progressFD)}
	start, _ := ParseTime(p.Output.Start)
	end, _ := ParseTime(p.Output.End)

	for i, in := range p.Inputs {
		if in.Proto == extract.ProtoYtDlp {
			if start > 0 {
				args = append(args, "-ss", ftoa(start))
			}
			args = append(args, "-i", "pipe:"+strconv.Itoa(inputFDs[i]))
			continue
		}
		args = append(args, extract.HeaderArgs(in.Headers, r.UserAgent)...)
		if r.Proxy != "" && strings.HasPrefix(r.Proxy, "http") {
			args = append(args, "-http_proxy", r.Proxy)
		}
		args = append(args, "-protocol_whitelist", "http,https,tcp,tls,crypto,data")
		if in.Proto == extract.ProtoHTTP {
			args = append(args, "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "10")
		}
		if in.Proto == extract.ProtoHLS {
			// Some CDNs disguise TS segments as images; allow any extension.
			args = append(args, "-allowed_extensions", "ALL")
			if r.HLSExtensionPicky {
				args = append(args, "-extension_picky", "0")
			}
		}
		if start > 0 && !in.Subtitle {
			args = append(args, "-ss", ftoa(start))
		}
		args = append(args, "-i", in.URL)
	}

	for _, m := range p.Maps {
		args = append(args, "-map", m)
	}
	for _, m := range p.SubMaps {
		args = append(args, "-map", m)
	}
	if end > 0 {
		args = append(args, "-t", ftoa(end-start))
	}

	o := p.Output
	if p.Container.AudioOnly || !p.hasVideo {
		args = append(args, "-vn")
	} else if p.copyVideo {
		args = append(args, "-c:v", "copy")
	} else {
		enc := videoEncoders[o.VideoCodec]
		args = append(args, "-c:v", enc)
		switch o.VideoCodec {
		case "h264":
			args = append(args, "-preset", cmp.Or(o.Preset, "veryfast"), "-crf", itoa(cmp.Or(o.CRF, 23)), "-pix_fmt", "yuv420p")
		case "h265":
			args = append(args, "-preset", cmp.Or(o.Preset, "fast"), "-crf", itoa(cmp.Or(o.CRF, 26)), "-pix_fmt", "yuv420p")
			if p.Container.ID == "mp4" || p.Container.ID == "mov" {
				args = append(args, "-tag:v", "hvc1")
			}
		case "vp9":
			args = append(args, "-crf", itoa(cmp.Or(o.CRF, 32)), "-b:v", "0", "-row-mt", "1", "-deadline", "good", "-cpu-used", "4")
		case "av1":
			if enc == "libsvtav1" {
				args = append(args, "-crf", itoa(cmp.Or(o.CRF, 35)), "-preset", "8")
			} else {
				args = append(args, "-crf", itoa(cmp.Or(o.CRF, 32)), "-b:v", "0", "-cpu-used", "6", "-row-mt", "1")
			}
		}
		if o.Height > 0 {
			args = append(args, "-vf", fmt.Sprintf(`scale=-2:min(%d\,ih)`, o.Height))
		}
	}

	copyAudio, audioCodec := p.copyAudio, o.AudioCodec
	// ADTS AAC from MPEG-TS: MP4/MOV need aac_adtstoasc; a non-seekable
	// Matroska output cannot write its header without the AAC config, so
	// the (cheap) audio is re-encoded when streaming.
	if copyAudio && p.audioFromHLS && streaming && p.Container.muxer == "matroska" {
		copyAudio, audioCodec = false, "aac"
	}
	if copyAudio {
		args = append(args, "-c:a", "copy")
		if p.audioFromHLS && (p.Container.muxer == "mp4" || p.Container.muxer == "mov") {
			args = append(args, "-bsf:a", "aac_adtstoasc")
		}
	} else {
		o.AudioCodec = audioCodec
		args = append(args, "-c:a", audioEncoders[o.AudioCodec])
		switch o.AudioCodec {
		case "aac", "mp3", "opus", "vorbis":
			def := map[string]int{"aac": 192, "mp3": 192, "opus": 128, "vorbis": 160}[o.AudioCodec]
			args = append(args, "-b:a", itoa(cmp.Or(o.AudioBitrate, def))+"k")
		}
		if o.AudioCodec == "opus" {
			// libopus does not accept 5.1(side) layouts and unusual sample rates.
			args = append(args, "-ac", "2", "-ar", "48000")
		}
	}
	if len(p.SubMaps) > 0 {
		args = append(args, "-c:s", p.Container.subCodec)
	}

	if p.Title != "" {
		args = append(args, "-metadata", "title="+p.Title)
	}
	args = append(args, "-f", p.Container.muxer)
	switch p.Container.muxer {
	case "mp4", "mov":
		if streaming {
			args = append(args, "-movflags", "frag_keyframe+empty_moov+default_base_moof")
		} else {
			args = append(args, "-movflags", "+faststart")
		}
	case "mpegts":
		args = append(args, "-mpegts_flags", "+resend_headers")
	}
	return append(args, dst)
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }
func itoa(i int) string     { return strconv.Itoa(i) }

// Run executes the plan, writing the result to w (streaming) or to the file
// dstPath when w is nil. onProgress is called periodically.
func (r *Runner) Run(ctx context.Context, p *Plan, cookies string, w io.Writer, dstPath string, onProgress func(Progress)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var extra []*os.File   // passed to ffmpeg
	var closers []*os.File // our copies, closed after start
	defer func() {
		for _, f := range closers {
			f.Close()
		}
	}()

	// yt-dlp producers: each writes one format into a pipe read by ffmpeg.
	type producer struct {
		cmd     *exec.Cmd
		stderr  *tailBuffer
		cleanup func()
	}
	var producers []producer
	inputFDs := map[int]int{}
	for i, in := range p.Inputs {
		if in.Proto != extract.ProtoYtDlp {
			continue
		}
		if r.YtDlp == nil {
			return extract.ErrYtDlpMissing
		}
		pr, pw, err := os.Pipe()
		if err != nil {
			return err
		}
		cmd, cleanup, err := r.YtDlp.Command(ctx, in.URL, in.FormatID, cookies)
		if err != nil {
			pr.Close()
			pw.Close()
			return err
		}
		tb := &tailBuffer{n: 4096}
		cmd.Stdout, cmd.Stderr = pw, tb
		if err := cmd.Start(); err != nil {
			cleanup()
			pr.Close()
			pw.Close()
			return fmt.Errorf("start yt-dlp: %w", err)
		}
		pw.Close() // the child holds its own copy
		producers = append(producers, producer{cmd: cmd, stderr: tb, cleanup: cleanup})
		extra = append(extra, pr)
		closers = append(closers, pr)
		inputFDs[i] = 2 + len(extra)
	}
	defer func() {
		cancel()
		for _, pd := range producers {
			_ = pd.cmd.Wait() // only reaps the process; its failure was reported while reading
			pd.cleanup()
		}
	}()

	progR, progW, err := os.Pipe()
	if err != nil {
		return err
	}
	extra = append(extra, progW)
	closers = append(closers, progW)
	progressFD := 2 + len(extra)

	dst := "pipe:1"
	if w == nil {
		dst = dstPath
	}
	args := r.Args(p, inputFDs, progressFD, dst, w != nil)
	cmd := exec.CommandContext(ctx, r.FFmpeg, args...)
	cmd.ExtraFiles = extra
	cmd.WaitDelay = 5 * time.Second
	stderr := &tailBuffer{n: 8192}
	cmd.Stderr = stderr
	if w != nil {
		cmd.Stdout = w
	}
	r.Log.Debug("ffmpeg", "args", redactArgs(args))
	if err := cmd.Start(); err != nil {
		progR.Close()
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	for _, f := range closers {
		f.Close()
	}
	closers = nil

	var wg sync.WaitGroup
	wg.Go(func() {
		defer progR.Close()
		readProgress(progR, p.Duration, onProgress)
	})
	err = cmd.Wait()
	wg.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil && err != nil {
		return ctxErr
	}
	if err != nil {
		msg := lastLine(stderr.String())
		for _, pd := range producers {
			if s := lastLine(pd.stderr.String()); s != "" {
				msg = strings.TrimSpace(msg + "; yt-dlp: " + s)
			}
		}
		if msg == "" {
			msg = err.Error()
		}
		return errors.New("ffmpeg: " + msg)
	}
	return nil
}

func readProgress(rd io.Reader, duration float64, onProgress func(Progress)) {
	sc := bufio.NewScanner(rd)
	var cur Progress
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "out_time_us", "out_time_ms": // both are microseconds
			if us, err := strconv.ParseInt(v, 10, 64); err == nil && us > 0 {
				cur.OutTime = float64(us) / 1e6
			}
		case "total_size":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				cur.Size = n
			}
		case "speed":
			cur.Speed, _ = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "x"), 64)
		case "progress":
			if duration > 0 {
				cur.Percent = min(100, cur.OutTime/duration*100)
			}
			if v == "end" && duration > 0 {
				cur.Percent = 100
			}
			if onProgress != nil {
				onProgress(cur)
			}
		}
	}
}

// redactArgs hides header values (cookies, tokens) in debug logs.
func redactArgs(args []string) string {
	var b bytes.Buffer
	for i, a := range args {
		if i > 0 && args[i-1] == "-headers" {
			a = "<redacted>"
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(a)
	}
	return b.String()
}
