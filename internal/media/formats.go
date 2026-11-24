// Package media converts extracted streams into files with an external ffmpeg
// binary (not bundled: install it and pass -ffmpeg). Output is written either
// straight into the HTTP response (pipe, no temp files) or into a temp file.
package media

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"slices"
	"strings"
)

// Container describes an output format.
type Container struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Ext         string   `json:"ext"`
	Mime        string   `json:"mime"`
	AudioOnly   bool     `json:"audio_only"`
	VideoCodecs []string `json:"video_codecs"`
	AudioCodecs []string `json:"audio_codecs"`
	Subtitles   bool     `json:"subtitles"`

	muxer    string
	subCodec string
}

// Containers lists the supported outputs. The first codec is the default.
var Containers = []Container{
	{ID: "mp4", Label: "MP4", Ext: "mp4", Mime: "video/mp4", muxer: "mp4", subCodec: "mov_text", Subtitles: true,
		VideoCodecs: []string{"copy", "h264", "h265", "av1"}, AudioCodecs: []string{"aac", "copy", "mp3", "opus"}},
	{ID: "mkv", Label: "MKV", Ext: "mkv", Mime: "video/x-matroska", muxer: "matroska", subCodec: "srt", Subtitles: true,
		VideoCodecs: []string{"copy", "h264", "h265", "vp9", "av1"}, AudioCodecs: []string{"copy", "aac", "opus", "flac", "mp3"}},
	{ID: "webm", Label: "WebM", Ext: "webm", Mime: "video/webm", muxer: "webm", subCodec: "webvtt", Subtitles: true,
		VideoCodecs: []string{"vp9", "av1", "copy"}, AudioCodecs: []string{"opus", "copy"}},
	{ID: "mov", Label: "MOV", Ext: "mov", Mime: "video/quicktime", muxer: "mov", subCodec: "mov_text", Subtitles: true,
		VideoCodecs: []string{"copy", "h264", "h265"}, AudioCodecs: []string{"aac", "copy"}},
	{ID: "ts", Label: "MPEG-TS", Ext: "ts", Mime: "video/mp2t", muxer: "mpegts",
		VideoCodecs: []string{"copy", "h264", "h265"}, AudioCodecs: []string{"aac", "copy", "mp3"}},
	{ID: "mp3", Label: "MP3", Ext: "mp3", Mime: "audio/mpeg", muxer: "mp3", AudioOnly: true, AudioCodecs: []string{"mp3"}},
	{ID: "m4a", Label: "M4A (AAC)", Ext: "m4a", Mime: "audio/mp4", muxer: "mp4", AudioOnly: true, AudioCodecs: []string{"aac", "copy"}},
	{ID: "opus", Label: "Opus", Ext: "opus", Mime: "audio/ogg", muxer: "opus", AudioOnly: true, AudioCodecs: []string{"opus", "copy"}},
	{ID: "ogg", Label: "OGG Vorbis", Ext: "ogg", Mime: "audio/ogg", muxer: "ogg", AudioOnly: true, AudioCodecs: []string{"vorbis", "opus"}},
	{ID: "flac", Label: "FLAC", Ext: "flac", Mime: "audio/flac", muxer: "flac", AudioOnly: true, AudioCodecs: []string{"flac"}},
	{ID: "wav", Label: "WAV", Ext: "wav", Mime: "audio/wav", muxer: "wav", AudioOnly: true, AudioCodecs: []string{"pcm"}},
}

// ContainerByID returns a container definition.
func ContainerByID(id string) (Container, bool) {
	i := slices.IndexFunc(Containers, func(c Container) bool { return c.ID == id })
	if i < 0 {
		return Container{}, false
	}
	return Containers[i], true
}

// codec name -> ffmpeg encoder
var videoEncoders = map[string]string{"h264": "libx264", "h265": "libx265", "vp9": "libvpx-vp9", "av1": "libsvtav1"}
var audioEncoders = map[string]string{"aac": "aac", "mp3": "libmp3lame", "opus": "libopus", "vorbis": "libvorbis", "flac": "flac", "pcm": "pcm_s16le"}

// Capabilities reports which codecs the installed ffmpeg can encode.
type Capabilities struct {
	VideoCodecs []string    `json:"video_codecs"`
	AudioCodecs []string    `json:"audio_codecs"`
	Containers  []Container `json:"containers"`
	Presets     []string    `json:"presets"`
	Heights     []int       `json:"heights"`
}

// DetectCapabilities runs `ffmpeg -encoders`.
func DetectCapabilities(ctx context.Context, ffmpeg string) Capabilities {
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders").Output()
	have := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && len(f[0]) == 6 {
			have[f[1]] = true
		}
	}
	// av1: accept any of the common encoders.
	if !have["libsvtav1"] && have["libaom-av1"] {
		videoEncoders["av1"] = "libaom-av1"
		have["libsvtav1"] = true
	}
	caps := Capabilities{
		VideoCodecs: []string{"copy"}, AudioCodecs: []string{"copy"},
		Presets: []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"},
		Heights: []int{0, 2160, 1440, 1080, 720, 480, 360, 240},
	}
	for _, c := range []string{"h264", "h265", "vp9", "av1"} {
		if have[videoEncoders[c]] {
			caps.VideoCodecs = append(caps.VideoCodecs, c)
		}
	}
	for _, c := range []string{"aac", "mp3", "opus", "vorbis", "flac", "pcm"} {
		if have[audioEncoders[c]] {
			caps.AudioCodecs = append(caps.AudioCodecs, c)
		}
	}
	for _, ct := range Containers {
		ct.VideoCodecs = filterAvail(ct.VideoCodecs, caps.VideoCodecs)
		ct.AudioCodecs = filterAvail(ct.AudioCodecs, caps.AudioCodecs)
		if len(ct.AudioCodecs) == 0 || !ct.AudioOnly && len(ct.VideoCodecs) == 0 {
			continue
		}
		caps.Containers = append(caps.Containers, ct)
	}
	return caps
}

func filterAvail(want, have []string) []string {
	var out []string
	for _, c := range want {
		if slices.Contains(have, c) {
			out = append(out, c)
		}
	}
	return out
}

// DetectHLSExtensionPicky reports whether the HLS demuxer supports -extension_picky.
func DetectHLSExtensionPicky(ctx context.Context, ffmpeg string) bool {
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-h", "demuxer=hls").CombinedOutput()
	return bytes.Contains(out, []byte("extension_picky"))
}
