package extract

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Prober inspects progressive/DASH sources with ffprobe to discover the
// embedded audio and subtitle streams (multiple dubs inside one MKV/MP4).
type Prober struct {
	Path  string
	Proxy string
	UA    string
}

type probeOut struct {
	Streams []struct {
		Index     int               `json:"index"`
		CodecType string            `json:"codec_type"`
		CodecName string            `json:"codec_name"`
		Width     int               `json:"width"`
		Height    int               `json:"height"`
		Channels  int               `json:"channels"`
		BitRate   string            `json:"bit_rate"`
		Tags      map[string]string `json:"tags"`
		Dispo     map[string]int    `json:"disposition"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

// HeaderArgs converts headers to ffmpeg/ffprobe input options.
func HeaderArgs(h Headers, ua string) []string {
	var args []string
	if v := h["User-Agent"]; v != "" {
		ua = v
	}
	if ua != "" {
		args = append(args, "-user_agent", ua)
	}
	var b strings.Builder
	for k, v := range h {
		if k == "User-Agent" || v == "" || strings.ContainsAny(k+v, "\r\n") {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	if b.Len() > 0 {
		args = append(args, "-headers", b.String())
	}
	return args
}

// Probe enriches the first format of st with stream information.
func (p *Prober) Probe(ctx context.Context, st *Stream) {
	if p == nil || p.Path == "" || len(st.Formats) == 0 {
		return
	}
	f := &st.Formats[0]
	if f.Protocol != ProtoHTTP && f.Protocol != ProtoDASH {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-print_format", "json", "-show_streams", "-show_format"}
	args = append(args, HeaderArgs(f.Headers, p.UA)...)
	if p.Proxy != "" && strings.HasPrefix(p.Proxy, "http") {
		args = append(args, "-http_proxy", p.Proxy)
	}
	args = append(args, "-protocol_whitelist", "http,https,tcp,tls,crypto", "-i", f.URL)
	cmd := exec.CommandContext(ctx, p.Path, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return
	}
	var po probeOut
	if json.Unmarshal(out.Bytes(), &po) != nil {
		return
	}
	if d, err := strconv.ParseFloat(po.Format.Duration, 64); err == nil && st.Duration == 0 {
		st.Duration = d
	}
	if s, err := strconv.ParseInt(po.Format.Size, 10, 64); err == nil && f.Size == 0 {
		f.Size = s
	}
	var audio, subs []Track
	ai, si := 0, 0
	f.HasAudio, f.HasVideo = false, false
	for _, s := range po.Streams {
		lang := s.Tags["language"]
		title := s.Tags["title"]
		switch s.CodecType {
		case "video":
			if s.Dispo["attached_pic"] == 1 {
				continue
			}
			f.HasVideo = true
			f.VCodec = s.CodecName
			if f.Height == 0 {
				f.Width, f.Height = s.Width, s.Height
				f.Label = qualityLabel(s.Height, 0)
			}
		case "audio":
			f.HasAudio = true
			if f.ACodec == "" {
				f.ACodec = s.CodecName
			}
			label := cmp.Or(title, lang, fmt.Sprintf("Track %d", ai+1))
			if title != "" && lang != "" {
				label = title + " (" + lang + ")"
			}
			br, _ := strconv.Atoi(s.BitRate)
			audio = append(audio, Track{
				ID: fmt.Sprintf("e%d", ai), Label: label, Lang: lang, Codec: s.CodecName, Bitrate: br,
				Default: s.Dispo["default"] == 1, Embedded: true, StreamIndex: ai,
			})
			ai++
		case "subtitle":
			switch s.CodecName {
			case "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle":
				si++
				continue // bitmap subtitles cannot be converted to text formats
			}
			subs = append(subs, Track{
				ID: fmt.Sprintf("es%d", si), Label: cmp.Or(title, lang, fmt.Sprintf("Subtitles %d", si+1)), Lang: lang,
				Codec: s.CodecName, Embedded: true, StreamIndex: si,
			})
			si++
		}
	}
	if len(audio) > 1 {
		st.Audio = append(audio, st.Audio...)
	}
	st.Subtitles = append(subs, st.Subtitles...)
}
