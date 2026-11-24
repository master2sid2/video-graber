package extract

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"videograber/internal/netx"
)

func TestJSToJSON(t *testing.T) {
	in := `[ { title:"Серия 1", // comment
		image: 'https://x/p.jpg', viewing: false , skip: [["354","369"]], n: -1.5, ok: !0,
		/* block */ file:"/a.php?x=1&y=it's", ref: someVar, },
	]`
	var v []map[string]any
	if err := json.Unmarshal([]byte(jsToJSON(in)), &v); err != nil {
		t.Fatalf("%v\n%s", err, jsToJSON(in))
	}
	if v[0]["title"] != "Серия 1" || v[0]["image"] != "https://x/p.jpg" || v[0]["ok"] != true || v[0]["ref"] != nil || v[0]["n"] != -1.5 {
		t.Fatalf("decoded: %#v", v[0])
	}
}

func TestJSPlaylistModule(t *testing.T) {
	page := `<html><body><script>
	var playlst=[
		{ title:"Серия 1", type_dub:'2', viewing: false ,
		  files:[{ title:"360", file:"https://cdn/e1/360.m3u8" },{ title:"720", file:"https://cdn/k=1,end=2/e1/720.mp4" }] },
		{ title:"Серия 2", files:[{ title:"360", file:"https://cdn/e2/360.m3u8" }] },
	];
	</script></body></html>`
	p := analyzePage([]byte(page), "https://site.example/player.php")
	if p.Playlist == nil || p.Playlist.Via != "js-playlist" || len(p.Playlist.Leaves) != 2 {
		t.Fatalf("playlist: %+v modules=%v", p.Playlist, p.Modules)
	}
	_, eps, _ := p.Playlist.choices()
	if len(eps) != 2 || eps[1].Number != 2 {
		t.Fatalf("episodes: %+v", eps)
	}
	items := parseLabeledList(p.Playlist.Leaves[0].File, nil)
	if len(items) != 2 || items[1].URL != "https://cdn/k=1,end=2/e1/720.mp4" {
		t.Fatalf("quality list: %+v", items)
	}
}

func TestQualityTitledLeavesMerge(t *testing.T) {
	pl := parsePjsJSON(`[{title:"360p",file:"https://a/360.m3u8"},{title:"1080p",file:"https://a/1080.m3u8"}]`, nil)
	if pl == nil || len(pl.Leaves) != 1 || !strings.Contains(pl.Leaves[0].File, "[1080p]") {
		t.Fatalf("quality-titled entries must become one source: %+v", pl)
	}
}

func TestScriptInjectedIframe(t *testing.T) {
	page := `<html><body><div id="v"></div><script>$("#v").html('<iframe src="/player.php?id=5" width="100%">');</script></body></html>`
	p := analyzePage([]byte(page), "https://site.example/show.html")
	if len(p.Iframes) != 1 || p.Iframes[0] != "https://site.example/player.php?id=5" {
		t.Fatalf("iframes: %v", p.Iframes)
	}
}

func TestDownloadLinksAreNotEpisodes(t *testing.T) {
	page := `<a href="/engine/gettorrent.php?id=1">Серия 1 (889 Mb)</a><a href="/engine/gettorrent.php?id=2">Серия 2 (880 Mb)</a>`
	if p := analyzePage([]byte(page), "https://site.example/"); len(p.Episodes) != 0 {
		t.Fatalf("torrent links taken as episodes: %+v", p.Episodes)
	}
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	c, _ := netx.New("", "test", true)
	return NewEngine(c, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestLegacyCharsetAndUnsupported(t *testing.T) {
	title, _ := charmap.Windows1251.NewEncoder().String("Магия")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1251")
		io.WriteString(w, `<html><head><title>`+title+`</title></head><body>no video here</body></html>`)
	}))
	defer srv.Close()

	_, err := newTestEngine(t).Extract(context.Background(), srv.URL, Options{})
	ue, ok := errors.AsType[*UnsupportedError](err)
	if !ok {
		t.Fatalf("expected UnsupportedError, got %v", err)
	}
	if len(ue.Tried) == 0 || !errors.Is(err, ErrNoMedia) {
		t.Fatalf("tried=%v err=%v", ue.Tried, err)
	}

	resp, err := newTestEngine(t).Client.Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := analyzePage(resp.Body, srv.URL); p.Title != "Магия" {
		t.Fatalf("windows-1251 title decoded as %q", p.Title)
	}
}
