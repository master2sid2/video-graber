package extract

import (
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"videograber/internal/netx"
)

// TestLive runs the engine against a real page: VG_LIVE=<url> go test -run TestLive -v
func TestLive(t *testing.T) {
	target := os.Getenv("VG_LIVE")
	if target == "" {
		t.Skip("set VG_LIVE=<url> to run")
	}
	c, err := netx.New("", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36", false)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewEngine(c, nil, nil, &Prober{Path: "ffprobe"}, log)
	res, err := e.Extract(t.Context(), target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := res.Resolve(t.Context(), res.Media.Selected)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.MarshalIndent(map[string]any{"media": res.Media, "stream": st}, "", "  ")
	t.Log(string(out))
	if sel := os.Getenv("VG_LIVE_SEL"); sel != "" {
		var s Selection
		json.Unmarshal([]byte(sel), &s)
		st2, err := res.Resolve(t.Context(), s)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.MarshalIndent(st2, "", "  ")
		t.Log("selection:", string(out))
		for _, f := range st2.Formats {
			t.Log(f.ID, f.URL)
		}
	}
}
