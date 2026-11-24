package media

import (
	"slices"
	"strings"
	"testing"

	"github.com/master2sid2/video-graber/internal/extract"
)

var testCaps = Capabilities{
	VideoCodecs: []string{"copy", "h264", "h265", "vp9", "av1"},
	AudioCodecs: []string{"copy", "aac", "mp3", "opus", "vorbis", "flac", "pcm"},
	Containers:  Containers,
}

func hlsStream() *extract.Stream {
	return &extract.Stream{
		Duration: 600,
		Formats: []extract.Format{
			{ID: "hls-1080", Height: 1080, Protocol: extract.ProtoHLS, URL: "https://cdn/1080.m3u8", HasVideo: true, HasAudio: true},
			{ID: "hls-480", Height: 480, Protocol: extract.ProtoHLS, URL: "https://cdn/480.m3u8", HasVideo: true, HasAudio: true},
		},
	}
}

func ytStream() *extract.Stream {
	return &extract.Stream{
		Duration: 100,
		Formats:  []extract.Format{{ID: "137", Height: 1080, Protocol: extract.ProtoYtDlp, URL: "https://yt/v", HasVideo: true, VCodec: "avc1"}},
		Audio: []extract.Track{
			{ID: "251", Label: "webm 128k · en", Lang: "en", Protocol: extract.ProtoYtDlp, URL: "https://yt/v"},
			{ID: "140", Label: "m4a 128k · de", Lang: "de", Protocol: extract.ProtoYtDlp, URL: "https://yt/v"},
		},
	}
}

func args(t *testing.T, p *Plan, streaming bool) string {
	t.Helper()
	r := &Runner{FFmpeg: "ffmpeg", UserAgent: "ua"}
	return strings.Join(r.Args(p, map[int]int{0: 3, 1: 4}, 5, "pipe:1", streaming), " ")
}

func TestParseTime(t *testing.T) {
	for in, want := range map[string]float64{"": 0, "90": 90, "1:30": 90, "1:02:03": 3723, "0:00.5": 0.5} {
		got, err := ParseTime(in)
		if err != nil || got != want {
			t.Errorf("ParseTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseTime("abc"); err == nil {
		t.Error("expected error")
	}
}

func TestPlanCopyMP4FromHLS(t *testing.T) {
	p, err := BuildPlan(hlsStream(), Pick{Format: "hls-480"}, Output{Container: "mp4", VideoCodec: "copy", AudioCodec: "copy"}, testCaps)
	if err != nil {
		t.Fatal(err)
	}
	a := args(t, p, true)
	for _, want := range []string{"-i https://cdn/480.m3u8", "-c:v copy", "-c:a copy", "-bsf:a aac_adtstoasc", "frag_keyframe+empty_moov", "-f mp4"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %s", want, a)
		}
	}
	if a2 := args(t, p, false); !strings.Contains(a2, "+faststart") {
		t.Errorf("file mode should use faststart: %s", a2)
	}
}

func TestPlanMKVStreamReencodesTSAudio(t *testing.T) {
	p, err := BuildPlan(hlsStream(), Pick{}, Output{Container: "mkv"}, testCaps)
	if err != nil {
		t.Fatal(err)
	}
	if a := args(t, p, true); !strings.Contains(a, "-c:a aac") {
		t.Errorf("streamed MKV from TS must re-encode audio: %s", a)
	}
	if a := args(t, p, false); !strings.Contains(a, "-c:a copy") {
		t.Errorf("MKV file may copy audio: %s", a)
	}
}

func TestPlanScaleForcesEncode(t *testing.T) {
	p, err := BuildPlan(hlsStream(), Pick{Format: "hls-1080"}, Output{Container: "mp4", VideoCodec: "copy", Height: 720, Start: "1:00", End: "1:30"}, testCaps)
	if err != nil {
		t.Fatal(err)
	}
	if p.Output.VideoCodec != "h264" || p.Duration != 30 {
		t.Fatalf("plan: codec=%s duration=%v", p.Output.VideoCodec, p.Duration)
	}
	a := args(t, p, true)
	for _, want := range []string{"-ss 60.000", "-t 30.000", "-c:v libx264", `scale=-2:min(720\,ih)`} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %s", want, a)
		}
	}
}

func TestPlanYtDlpSeparateAudio(t *testing.T) {
	p, err := BuildPlan(ytStream(), Pick{}, Output{Container: "mp4"}, testCaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Inputs) != 2 || p.Inputs[1].FormatID != "251" {
		t.Fatalf("expected video + default audio inputs: %+v", p.Inputs)
	}
	if !slices.Equal(p.Maps, []string{"0:v:0", "1:a:0"}) {
		t.Errorf("maps: %v", p.Maps)
	}
	if a := args(t, p, true); !strings.Contains(a, "-i pipe:3") || !strings.Contains(a, "-i pipe:4") {
		t.Errorf("yt-dlp inputs must be pipes: %s", a)
	}
}

func TestPlanAudioOnlyUsesOnlyAudioInput(t *testing.T) {
	p, err := BuildPlan(ytStream(), Pick{Audio: []string{"140"}}, Output{Container: "mp3"}, testCaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Inputs) != 1 || p.Inputs[0].FormatID != "140" {
		t.Fatalf("audio-only must not download video: %+v", p.Inputs)
	}
	a := args(t, p, true)
	if !strings.Contains(a, "-vn") || !strings.Contains(a, "libmp3lame") {
		t.Errorf("args: %s", a)
	}
}

func TestPlanBatchFallbacks(t *testing.T) {
	// Format ids of another episode differ: fall back to the preferred height
	// and pick the audio track by language.
	p, err := BuildPlan(ytStream(), Pick{Format: "999", Quality: 720, Audio: []string{"nope"}, AudioLang: "de"}, Output{Container: "mkv"}, testCaps)
	if err != nil {
		t.Fatal(err)
	}
	if p.Inputs[0].FormatID != "137" || p.Inputs[1].FormatID != "140" {
		t.Fatalf("inputs: %+v", p.Inputs)
	}
}

func TestPlanErrors(t *testing.T) {
	if _, err := BuildPlan(hlsStream(), Pick{Format: "zzz"}, Output{Container: "mp4"}, testCaps); err == nil {
		t.Error("unknown format must fail")
	}
	if _, err := BuildPlan(hlsStream(), Pick{}, Output{Container: "exe"}, testCaps); err == nil {
		t.Error("unknown container must fail")
	}
	if _, err := BuildPlan(hlsStream(), Pick{}, Output{Container: "mp4", Start: "2:00", End: "1:00"}, testCaps); err == nil {
		t.Error("end before start must fail")
	}
}

func TestWebMCopyOfH264Reencodes(t *testing.T) {
	st := ytStream()
	p, err := BuildPlan(st, Pick{}, Output{Container: "webm", VideoCodec: "copy"}, testCaps)
	if err != nil {
		t.Fatal(err)
	}
	if p.Output.VideoCodec != "vp9" {
		t.Errorf("h264 cannot be copied into WebM, got %s", p.Output.VideoCodec)
	}
}
