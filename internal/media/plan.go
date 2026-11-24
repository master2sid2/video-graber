package media

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/master2sid2/video-graber/internal/extract"
)

// Output holds the user's conversion settings.
type Output struct {
	Container    string `json:"container"`
	VideoCodec   string `json:"video_codec,omitempty"`
	AudioCodec   string `json:"audio_codec,omitempty"`
	Height       int    `json:"height,omitzero"`        // 0 = keep
	CRF          int    `json:"crf,omitzero"`           // 0 = codec default
	Preset       string `json:"preset,omitempty"`       // x264/x265 preset
	AudioBitrate int    `json:"audio_bitrate,omitzero"` // kbps, 0 = default
	Start        string `json:"start,omitempty"`        // trim start, [[hh:]mm:]ss
	End          string `json:"end,omitempty"`          // trim end
}

// Pick chooses what to download from a stream.
type Pick struct {
	Format    string   `json:"format"`              // format id ("" = best)
	Quality   int      `json:"quality,omitzero"`    // preferred height when Format is unknown (batch downloads)
	Audio     []string `json:"audio,omitempty"`     // audio track ids ("" = default)
	AudioLang string   `json:"audio_lang,omitzero"` // preferred audio label/lang when ids are unknown
	Subtitles []string `json:"subtitles,omitempty"` // subtitle track ids
}

// Input is one ffmpeg input.
type Input struct {
	Proto    extract.Protocol
	URL      string
	Headers  extract.Headers
	FormatID string // for yt-dlp inputs
	Subtitle bool
}

// Plan is a fully resolved conversion.
type Plan struct {
	Inputs    []Input
	Maps      []string
	SubMaps   []string
	Container Container
	Output    Output
	Duration  float64 // of the produced media, for progress
	Title     string
	copyVideo bool
	copyAudio bool
	hasVideo  bool
	// audioFromHLS is set when copied audio comes from MPEG-TS segments
	// (ADTS AAC needs the aac_adtstoasc bitstream filter for MKV/MP4).
	audioFromHLS bool
}

var reTime = regexp.MustCompile(`^(?:(\d+):)?(?:(\d+):)?(\d+(?:\.\d+)?)$`)

// ParseTime parses [[hh:]mm:]ss[.ms] into seconds.
func ParseTime(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	m := reTime.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	parts := []string{m[1], m[2]}
	var total float64
	sec, _ := strconv.ParseFloat(m[3], 64)
	nums := []float64{}
	for _, p := range parts {
		if p != "" {
			v, _ := strconv.ParseFloat(p, 64)
			nums = append(nums, v)
		}
	}
	switch len(nums) {
	case 1:
		total = nums[0]*60 + sec
	case 2:
		total = nums[0]*3600 + nums[1]*60 + sec
	default:
		total = sec
	}
	return total, nil
}

func pickFormat(st *extract.Stream, p Pick) (*extract.Format, error) {
	if p.Format != "" {
		if f, ok := st.FindFormat(p.Format); ok {
			return f, nil
		}
		if p.Quality == 0 {
			return nil, fmt.Errorf("unknown format %q", p.Format)
		}
	}
	if p.Quality > 0 {
		// Highest format not above the wanted height, else the smallest one.
		best := -1
		for i, f := range st.Formats {
			if f.Height <= p.Quality && (best < 0 || f.Height > st.Formats[best].Height) {
				best = i
			}
		}
		if best >= 0 {
			return &st.Formats[best], nil
		}
		return &st.Formats[len(st.Formats)-1], nil
	}
	return &st.Formats[0], nil
}

func defaultAudio(st *extract.Stream, f *extract.Format, lang string) *extract.Track {
	if len(st.Audio) == 0 {
		return nil
	}
	if lang != "" {
		l := strings.ToLower(lang)
		for i := range st.Audio {
			a := &st.Audio[i]
			if strings.EqualFold(a.Label, lang) || strings.EqualFold(a.Lang, lang) || strings.Contains(strings.ToLower(a.Label), l) {
				return a
			}
		}
	}
	var group []*extract.Track
	for i := range st.Audio {
		if f.AudioGroup == "" || st.Audio[i].Group == f.AudioGroup {
			group = append(group, &st.Audio[i])
		}
	}
	if len(group) == 0 {
		return nil
	}
	if i := slices.IndexFunc(group, func(t *extract.Track) bool { return t.Default }); i >= 0 {
		return group[i]
	}
	return group[0]
}

// BuildPlan validates a pick + output against a stream.
func BuildPlan(st *extract.Stream, p Pick, out Output, caps Capabilities) (*Plan, error) {
	if st == nil || len(st.Formats) == 0 {
		return nil, extract.ErrNoMedia
	}
	ct, ok := ContainerByID(out.Container)
	if !ok {
		return nil, fmt.Errorf("unknown container %q", out.Container)
	}
	if i := slices.IndexFunc(caps.Containers, func(c Container) bool { return c.ID == ct.ID }); i >= 0 {
		ct = caps.Containers[i]
	}
	f, err := pickFormat(st, p)
	if err != nil {
		return nil, err
	}
	start, err := ParseTime(out.Start)
	if err != nil {
		return nil, err
	}
	end, err := ParseTime(out.End)
	if err != nil {
		return nil, err
	}
	if end > 0 && end <= start {
		return nil, errors.New("trim end must be after start")
	}

	plan := &Plan{Container: ct, Output: out, Title: st.Title}
	plan.Duration = st.Duration
	if end > 0 {
		plan.Duration = end - start
	} else if start > 0 && st.Duration > start {
		plan.Duration = st.Duration - start
	}

	// Resolve audio tracks.
	var tracks []*extract.Track
	for _, id := range p.Audio {
		if id == "" {
			continue
		}
		t, ok := st.FindAudio(id)
		if !ok {
			if p.AudioLang == "" {
				return nil, fmt.Errorf("unknown audio track %q", id)
			}
			continue
		}
		tracks = append(tracks, t)
	}
	if len(tracks) == 0 && (!f.HasAudio || p.AudioLang != "") {
		if t := defaultAudio(st, f, p.AudioLang); t != nil && (!t.Embedded || !f.HasAudio || p.AudioLang != "") {
			tracks = append(tracks, t)
		}
	}
	needsVideoInput := !ct.AudioOnly
	for _, t := range tracks {
		if t.Embedded {
			needsVideoInput = true
		}
	}
	if ct.AudioOnly && len(tracks) == 0 {
		if !f.HasAudio {
			return nil, errors.New("this source has no audio")
		}
		needsVideoInput = true
	}
	if !ct.AudioOnly && !f.HasVideo {
		return nil, errors.New("the selected format has no video; choose an audio container")
	}

	input := func(proto extract.Protocol, u string, h extract.Headers, fid string, sub bool) int {
		plan.Inputs = append(plan.Inputs, Input{Proto: proto, URL: u, Headers: h, FormatID: fid, Subtitle: sub})
		return len(plan.Inputs) - 1
	}
	main := -1
	if needsVideoInput {
		main = input(f.Protocol, f.URL, f.Headers, f.ID, false)
		if !ct.AudioOnly {
			plan.Maps = append(plan.Maps, fmt.Sprintf("%d:v:0", main))
			plan.hasVideo = true
		}
	}
	if len(tracks) == 0 {
		plan.Maps = append(plan.Maps, fmt.Sprintf("%d:a:0?", main))
		plan.audioFromHLS = f.Protocol == extract.ProtoHLS
	}
	for _, t := range tracks {
		if t.Embedded {
			plan.Maps = append(plan.Maps, fmt.Sprintf("%d:a:%d", main, t.StreamIndex))
			plan.audioFromHLS = plan.audioFromHLS || f.Protocol == extract.ProtoHLS
			continue
		}
		plan.audioFromHLS = plan.audioFromHLS || t.Protocol == extract.ProtoHLS
		i := input(t.Protocol, t.URL, t.Headers, t.ID, false)
		plan.Maps = append(plan.Maps, fmt.Sprintf("%d:a:0", i))
	}

	// Subtitles (video containers only).
	if ct.Subtitles {
		for _, id := range p.Subtitles {
			t, ok := st.FindSubtitle(id)
			if !ok {
				continue
			}
			if t.Embedded {
				if main >= 0 {
					plan.SubMaps = append(plan.SubMaps, fmt.Sprintf("%d:s:%d", main, t.StreamIndex))
				}
				continue
			}
			i := input(t.Protocol, t.URL, t.Headers, t.ID, true)
			plan.SubMaps = append(plan.SubMaps, fmt.Sprintf("%d:s:0", i))
		}
	}

	// Codecs.
	if !ct.AudioOnly {
		vc := out.VideoCodec
		if vc == "" || !slices.Contains(ct.VideoCodecs, vc) {
			vc = ct.VideoCodecs[0]
		}
		if vc == "copy" && out.Height > 0 && (f.Height == 0 || out.Height < f.Height) {
			vc = firstNonCopy(ct.VideoCodecs)
		}
		if vc == "copy" && ct.ID == "webm" && f.VCodec != "" && !strings.HasPrefix(f.VCodec, "vp") && !strings.HasPrefix(f.VCodec, "av01") {
			vc = firstNonCopy(ct.VideoCodecs)
		}
		if vc == "" {
			return nil, errors.New("no suitable video encoder available in ffmpeg")
		}
		plan.Output.VideoCodec = vc
		plan.copyVideo = vc == "copy"
	}
	ac := out.AudioCodec
	if ac == "" || !slices.Contains(ct.AudioCodecs, ac) {
		ac = ct.AudioCodecs[0]
	}
	plan.Output.AudioCodec = ac
	plan.copyAudio = ac == "copy"
	return plan, nil
}

func firstNonCopy(codecs []string) string {
	for _, c := range codecs {
		if c != "copy" {
			return c
		}
	}
	return ""
}
