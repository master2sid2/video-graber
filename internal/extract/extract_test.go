package extract

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"videograber/internal/netx"
)

func TestParseHLSMaster(t *testing.T) {
	data := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Russian, dub",LANGUAGE="ru",DEFAULT=YES,URI="audio/ru.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Original",LANGUAGE="ko",URI="audio/ko.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="en",URI="subs/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1796000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud",SUBTITLES="subs"
1080/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=700000,RESOLUTION=854x480,AUDIO="aud"
https://cdn.example/480/index.m3u8
`
	p, err := parseHLS([]byte(data), "https://cdn.example/show/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Master || len(p.Variants) != 2 || len(p.Media) != 3 {
		t.Fatalf("unexpected playlist: %+v", p)
	}
	if got := p.Variants[0].URL; got != "https://cdn.example/show/1080/index.m3u8" {
		t.Errorf("relative variant URL resolved to %q", got)
	}
	if p.Variants[0].Height != 1080 || p.Variants[0].Audio != "aud" {
		t.Errorf("variant attrs: %+v", p.Variants[0])
	}
	if p.Media[0].Name != "Russian, dub" || !p.Media[0].Default || p.Media[0].URI != "https://cdn.example/show/audio/ru.m3u8" {
		t.Errorf("quoted attribute with comma parsed wrong: %+v", p.Media[0])
	}
}

func TestParseHLSMedia(t *testing.T) {
	p, err := parseHLS([]byte("#EXTM3U\n#EXTINF:10.5,\na.ts\n#EXTINF:4.5,\nb.ts\n#EXT-X-ENDLIST\n"), "https://x/y.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if p.Master || p.Duration != 15 {
		t.Fatalf("got master=%v duration=%v", p.Master, p.Duration)
	}
	if _, err := parseHLS([]byte("<html>"), ""); err == nil {
		t.Fatal("expected error for non-playlist")
	}
}

func TestParseLabeledList(t *testing.T) {
	base, _ := url.Parse("https://site.example/player/")
	tests := []struct {
		in   string
		want []labeled
	}{
		{"[360p]https://a/360.mp4,[720p]//a/720.mp4", []labeled{{"360p", "https://a/360.mp4"}, {"720p", "https://a/720.mp4"}}},
		{"https://a/1.m3u8 or https://b/1.m3u8", []labeled{{"", "https://a/1.m3u8"}}},
		{"/v/file.mp4", []labeled{{"", "https://site.example/v/file.mp4"}}},
	}
	for _, tt := range tests {
		got := parseLabeledList(tt.in, base)
		if len(got) != len(tt.want) {
			t.Fatalf("%q: got %v", tt.in, got)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%q[%d]: got %v want %v", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestPlayerjsPlaylist(t *testing.T) {
	base, _ := url.Parse("https://p.example/")
	raw := `var player = new Playerjs({id:"player", file:'[{"title":"Dub A","folder":[{"title":"Season 1","folder":[` +
		`{"title":"Episode 1","file":"[720p]https://cdn/a1.m3u8"},{"title":"Episode 2","file":"https://cdn/a2.m3u8"}]}]},` +
		`{"title":"Dub B","folder":[{"title":"Season 1","folder":[{"title":"Episode 1","file":"https://cdn/b1.m3u8"}]}]}]'});`
	pl, enc := findPlayerjs(raw, base)
	if pl == nil || enc {
		t.Fatalf("playlist not found (encrypted=%v)", enc)
	}
	tr, eps, _ := pl.choices()
	if len(tr) != 2 || tr[1].Label != "Dub B" {
		t.Fatalf("translations: %+v", tr)
	}
	if len(eps) != 2 || eps[0].Season != "Season 1" || eps[1].Number != 2 {
		t.Fatalf("episodes: %+v", eps)
	}
	leaf := pl.pick(Selection{Translation: tr[1].ID, Episode: eps[0].ID})
	if leaf.File != "https://cdn/b1.m3u8" {
		t.Errorf("pick returned %q", leaf.File)
	}
	if _, enc := findPlayerjs(`new Playerjs({file:"#2aHR0cHM6Ly9leGFtcGxl"})`, base); !enc {
		t.Error("obfuscated config not flagged")
	}
}

func TestEpisodeName(t *testing.T) {
	cases := []struct{ title, ep, want string }{
		{"Некий ITEM тёмной стороны / Toaru Anbu no Item", "Серия 1", "Некий ITEM тёмной стороны / Toaru Anbu no Item - Серия 1"},
		{"Дорама Демон / Гоблин 1 серия русская озвучка", "Демон / Гоблин 2 серия", "Демон / Гоблин 2 серия"},
		{"Show", "", "Show"},
		{"", "Episode 3", "Episode 3"},
		{"Big Show", "Episode 3", "Big Show - Episode 3"},
	}
	for _, c := range cases {
		if got := EpisodeName(c.title, c.ep); got != c.want {
			t.Errorf("EpisodeName(%q, %q) = %q, want %q", c.title, c.ep, got, c.want)
		}
	}
}

func TestEpisodeDetection(t *testing.T) {
	cases := map[string]int{
		"Демон / Гоблин 12 серия": 12, "Episode 7": 7, "3-я серия": 3, "EP 05": 5, "Серия 10": 10,
	}
	for in, want := range cases {
		if got, ok := episodeNumber(in); !ok || got != want {
			t.Errorf("episodeNumber(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	if _, ok := episodeNumber("Дорама Гоблин: 10-я годовщина"); ok {
		t.Error("anniversary special must not count as an episode")
	}
	eps := []episodeLink{
		{Label: "Show 1 серия", URL: "u1", Number: 1}, {Label: "Show 2 серия", URL: "u2", Number: 2},
		{Label: "Show 3 серия", URL: "u3", Number: 3}, {Label: "Other 5 серия", URL: "x5", Number: 5},
	}
	for i := range eps {
		eps[i].group = episodeGroup(eps[i].Label)
	}
	got := filterEpisodes(eps, "")
	if len(got) != 3 || got[2].URL != "u3" {
		t.Fatalf("filterEpisodes kept %+v", got)
	}
}

// TestGenericPageWithPlayers serves a small site: a page with two voice-over
// tabs pointing at player iframes, each exposing an HLS master playlist.
func TestGenericPageWithPlayers(t *testing.T) {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/show-1-seriya", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<html><head><meta property="og:title" content="Show"><meta property="og:image" content="/p.jpg"></head><body>
		<a href="/show-1-seriya" title="Show 1 серия">1</a><a href="/show-2-seriya" title="Show 2 серия">2</a>
		<div><a data-url-player="//`+r.Host+`/player?v=a" aria-selected="true"><h3>Dub A</h3></a>
		<a data-url-player="//`+r.Host+`/player?v=b"><h3>Dub B</h3></a></div>
		<iframe data-src="//`+r.Host+`/player?v=a"></iframe></body></html>`)
	})
	mux.HandleFunc("/player", func(w http.ResponseWriter, r *http.Request) {
		if r.Referer() == "" {
			http.Error(w, "no referer", http.StatusForbidden)
			return
		}
		v := r.URL.Query().Get("v")
		io.WriteString(w, `<div data-config='{"hls":"`+strings.ReplaceAll(srv.URL, "/", `\/`)+`\/hls\/`+v+`\/index.m3u8"}'></div>`)
	})
	mux.HandleFunc("/hls/{v}/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=1280x720\n720.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=400000,RESOLUTION=640x360\n360.m3u8\n")
	})
	mux.HandleFunc("/hls/{v}/720.m3u8", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXTINF:6,\n0.ts\n#EXTINF:6,\n1.ts\n#EXT-X-ENDLIST\n")
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	c, _ := netx.New("", "test", true)
	e := NewEngine(c, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	res, err := e.Extract(context.Background(), srv.URL+"/show-1-seriya", Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Media
	if m.Title != "Show" || len(m.Translations) != 2 || len(m.Episodes) != 2 {
		t.Fatalf("media: %+v", m)
	}
	if m.Selected.Episode != "e1" || m.Selected.Translation != "dub-a" {
		t.Errorf("selected: %+v", m.Selected)
	}
	st, err := res.Resolve(context.Background(), Selection{Translation: "dub-b", Episode: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Formats) != 2 || st.Formats[0].Height != 720 || st.Duration != 12 {
		t.Fatalf("stream: %+v", st)
	}
	if !strings.Contains(st.Formats[0].URL, "/hls/b/") {
		t.Errorf("translation not applied: %s", st.Formats[0].URL)
	}
	if st.Formats[0].Headers["Referer"] == "" {
		t.Error("media requests need the player page as Referer")
	}
}

func TestPrivateAddressBlocked(t *testing.T) {
	c, _ := netx.New("", "test", false)
	e := NewEngine(c, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := e.Extract(context.Background(), "http://127.0.0.1:1/", Options{}); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("expected private address error, got %v", err)
	}
}
