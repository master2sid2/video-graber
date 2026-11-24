package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"videograber/internal/auth"
	"videograber/internal/extract"
	"videograber/internal/jobs"
	"videograber/internal/media"
	"videograber/internal/store"
)

// mediaSession keeps an extraction result so that the UI can switch
// episodes/translations and start jobs without re-analysing the page.
type mediaSession struct {
	id      string
	owner   int64
	result  *extract.Result
	cookies string

	mu      sync.Mutex
	used    time.Time
	streams map[extract.Selection]cachedStream
}

type cachedStream struct {
	st *extract.Stream
	at time.Time
}

// streamTTL bounds how long resolved URLs are reused: CDN links often carry
// short-lived tokens.
const streamTTL = 10 * time.Minute

func (m *mediaSession) lastUsed() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.used
}

// stream resolves (and caches) the stream of a selection.
func (m *mediaSession) stream(ctx context.Context, sel extract.Selection) (*extract.Stream, error) {
	m.mu.Lock()
	m.used = time.Now()
	if c, ok := m.streams[sel]; ok && time.Since(c.at) < streamTTL {
		m.mu.Unlock()
		return c.st, nil
	}
	m.mu.Unlock()
	st, err := m.result.Resolve(ctx, sel)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.streams[sel] = cachedStream{st: st, at: time.Now()}
	m.mu.Unlock()
	return st, nil
}

func (m *mediaSession) label(sel extract.Selection) (episode, translation string) {
	for _, c := range m.result.Media.Episodes {
		if c.ID == sel.Episode {
			episode = c.Label
		}
	}
	for _, c := range m.result.Media.Translations {
		if c.ID == sel.Translation {
			translation = c.Label
		}
	}
	return
}

func ownerOf(r *http.Request) int64 {
	if u := userFrom(r); u != nil {
		return u.ID
	}
	return 0
}

func (s *Server) session(r *http.Request, id string) (*mediaSession, bool) {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	ms, ok := s.sessions[id]
	if !ok || ms.owner != ownerOf(r) {
		return nil, false
	}
	return ms, true
}

type extractResponse struct {
	ID     string          `json:"id"`
	Media  *extract.Media  `json:"media"`
	Stream *extract.Stream `json:"stream"`
}

func (s *Server) handleExtract(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL       string `json:"url"`
		Extractor string `json:"extractor,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		writeError(w, http.StatusBadRequest, "URL is required")
		return
	}
	select {
	case s.extractSem <- struct{}{}:
		defer func() { <-s.extractSem }()
	case <-r.Context().Done():
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ExtractTimeout)
	defer cancel()
	cookies := s.cookiesFor(r)
	start := time.Now()
	res, err := s.engine.Extract(ctx, req.URL, extract.Options{Cookies: cookies, Force: req.Extractor})
	if err != nil {
		s.log.Info("extraction failed", "url", req.URL, "err", err, "took", time.Since(start).Round(time.Millisecond))
		if ue, ok := errors.AsType[*extract.UnsupportedError](err); ok {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": ue.Error(), "code": "unsupported", "tried": ue.Tried, "detail": friendlyError(ue.Cause),
			})
			return
		}
		writeError(w, http.StatusUnprocessableEntity, friendlyError(err))
		return
	}
	ms := &mediaSession{
		id: auth.RandomHex(10), owner: ownerOf(r), result: res, cookies: cookies,
		used: time.Now(), streams: map[extract.Selection]cachedStream{},
	}
	st, err := ms.stream(ctx, res.Media.Selected)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, friendlyError(err))
		return
	}
	if res.Media.Duration == 0 {
		res.Media.Duration = st.Duration
	}
	s.sessMu.Lock()
	s.sessions[ms.id] = ms
	s.sessMu.Unlock()
	s.log.Info("extracted", "url", req.URL, "extractor", res.Media.Extractor, "formats", len(st.Formats),
		"episodes", len(res.Media.Episodes), "translations", len(res.Media.Translations), "took", time.Since(start).Round(time.Millisecond))
	writeJSON(w, http.StatusOK, extractResponse{ID: ms.id, Media: res.Media, Stream: st})
}

// friendlyError shortens joined errors to their first meaningful lines.
func friendlyError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "extraction timed out"
	}
	lines := strings.Split(err.Error(), "\n")
	var out []string
	seen := map[string]bool{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
		if len(out) == 4 {
			break
		}
	}
	return strings.Join(out, "; ")
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.session(r, r.PathValue("id"))
	if !ok {
		writeExpired(w)
		return
	}
	var sel extract.Selection
	if err := readJSON(r, &sel); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ExtractTimeout)
	defer cancel()
	st, err := ms.stream(ctx, sel)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, friendlyError(err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ---------- jobs ----------

type createJobsRequest struct {
	MediaID    string              `json:"media_id"`
	Items      []extract.Selection `json:"items"`
	Pick       media.Pick          `json:"pick"`
	Output     media.Output        `json:"output"`
	Mode       jobs.Mode           `json:"mode"`
	CategoryID *int64              `json:"category_id"`
}

func (s *Server) handleCreateJobs(w http.ResponseWriter, r *http.Request) {
	var req createJobsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ms, ok := s.session(r, req.MediaID)
	if !ok {
		writeExpired(w)
		return
	}
	if req.Mode != jobs.ModeFile {
		req.Mode = jobs.ModeStream
	}
	ct, ok := media.ContainerByID(req.Output.Container)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown output container")
		return
	}
	if len(req.Items) == 0 {
		req.Items = []extract.Selection{ms.result.Media.Selected}
	}
	if len(req.Items) > 500 {
		writeError(w, http.StatusBadRequest, "too many items")
		return
	}
	u := userFrom(r)
	if req.CategoryID != nil && (u == nil || !s.store.CategoryOwned(r.Context(), u.ID, *req.CategoryID)) {
		req.CategoryID = nil
	}

	// Validate the first item synchronously so that obvious mistakes
	// (unknown format, no video) are reported right away.
	first, err := ms.stream(r.Context(), req.Items[0])
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, friendlyError(err))
		return
	}
	if _, err := media.BuildPlan(first, req.Pick, req.Output, s.caps); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	m := ms.result.Media
	var created []*jobs.Job
	for _, sel := range req.Items {
		episode, translation := ms.label(sel)
		pick, out := req.Pick, req.Output
		j := &jobs.Job{
			Title: m.Title, Subtitle: strings.Join(nonEmpty(episode, translation), " · "), Thumbnail: m.Thumbnail,
			PageURL: m.PageURL, Mode: req.Mode, Output: out, Owner: ownerOf(r), Cookies: ms.cookies,
		}
		j.Filename = jobs.SafeFilename(extract.EpisodeName(m.Title, episode), translation) + "." + ct.Ext
		j.Prepare = func(ctx context.Context) (*media.Plan, error) {
			ctx, cancel := context.WithTimeout(ctx, s.cfg.ExtractTimeout)
			defer cancel()
			st, err := ms.stream(ctx, sel)
			if err != nil {
				return nil, err
			}
			plan, err := media.BuildPlan(st, pick, out, s.caps)
			if err != nil {
				return nil, err
			}
			for _, in := range plan.Inputs {
				if in.Proto == extract.ProtoYtDlp {
					continue
				}
				if err := s.client.CheckURL(ctx, in.URL); err != nil {
					return nil, err
				}
			}
			plan.Title = strings.Join(nonEmpty(m.Title, episode), " — ")
			return plan, nil
		}
		if u != nil {
			opts, _ := json.Marshal(map[string]any{
				"extractor": m.Extractor, "selection": sel, "pick": pick, "output": out, "mode": req.Mode,
				"episode": episode, "translation": translation,
			})
			hid, err := s.store.AddHistory(r.Context(), u.ID, &store.HistoryItem{
				CategoryID: req.CategoryID, PageURL: m.PageURL, Title: m.Title, Subtitle: j.Subtitle,
				Thumbnail: m.Thumbnail, Extractor: m.Extractor, Options: string(opts), Status: "pending",
			})
			if err != nil {
				s.log.Warn("cannot record history", "err", err)
			}
			j.HistoryID = hid
		}
		created = append(created, s.jobs.Add(j))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"jobs": created})
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.jobs.List(ownerOf(r)))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.Snapshot(r.PathValue("id"), ownerOf(r))
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if err := s.jobs.Cancel(r.PathValue("id"), ownerOf(r)); err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	if err := s.jobs.Remove(r.PathValue("id"), ownerOf(r)); err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.Get(r.PathValue("id"), ownerOf(r))
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	s.jobs.Serve(w, r, j)
}

// handleEvents streams job updates with Server-Sent Events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = rc.SetWriteDeadline(time.Time{})

	owner := ownerOf(r)
	ch, unsubscribe := s.jobs.Subscribe(owner)
	defer unsubscribe()

	send := func(event string, v any) error {
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return err
		}
		return rc.Flush()
	}
	if send("snapshot", s.jobs.List(owner)) != nil {
		return
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if send(ev.Type, ev) != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

// writeExpired reports a media session that no longer exists (expired or
// lost on restart); the UI re-analyses the page and restores the selection.
func writeExpired(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "this analysis has expired, please grab the URL again", "code": "expired"})
}
