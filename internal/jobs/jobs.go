// Package jobs manages download/conversion jobs.
//
// Two delivery modes exist:
//
//   - stream: nothing runs until the browser requests the download; ffmpeg
//     output is piped straight into the HTTP response. No temp files.
//   - file: ffmpeg writes a temp file in the background (needed when the
//     container requires seeking, e.g. MP4 with faststart, or to get a
//     Content-Length). The file is deleted once fully downloaded, or after
//     the configured TTL.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"videograber/internal/auth"
	"videograber/internal/media"
)

// State of a job.
type State string

const (
	StatePending  State = "pending"  // stream mode: waiting for the browser
	StateQueued   State = "queued"   // waiting for a free worker slot
	StateRunning  State = "running"  // ffmpeg is working
	StateReady    State = "ready"    // file mode: temp file prepared
	StateDone     State = "done"     // delivered
	StateFailed   State = "failed"   // error
	StateCanceled State = "canceled" // stopped by the user or disconnect
	StateExpired  State = "expired"  // temp file deleted before download
)

// Terminal reports whether no more work will happen for the state.
func (s State) Terminal() bool {
	return s == StateDone || s == StateFailed || s == StateCanceled || s == StateExpired
}

// Mode is the delivery mode.
type Mode string

const (
	ModeStream Mode = "stream"
	ModeFile   Mode = "file"
)

// Preparer resolves the stream and builds the ffmpeg plan when work starts.
type Preparer func(ctx context.Context) (*media.Plan, error)

// Job is one download.
type Job struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Subtitle  string         `json:"subtitle,omitempty"`
	Thumbnail string         `json:"thumbnail,omitempty"`
	PageURL   string         `json:"page_url"`
	Filename  string         `json:"filename"`
	Mode      Mode           `json:"mode"`
	State     State          `json:"state"`
	Progress  media.Progress `json:"progress"`
	Error     string         `json:"error,omitempty"`
	Size      int64          `json:"size,omitzero"`
	Output    media.Output   `json:"output"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`

	Owner     int64    `json:"-"`
	HistoryID int64    `json:"-"`
	Cookies   string   `json:"-"`
	Prepare   Preparer `json:"-"`

	tempPath   string
	cancel     context.CancelFunc
	lastNotify time.Time
	removed    bool
}

// Event is pushed to subscribers.
type Event struct {
	Type string `json:"type"` // "job" or "removed"
	Job  *Job   `json:"job,omitempty"`
	ID   string `json:"id,omitempty"`

	owner int64
}

type subscriber struct {
	ch    chan Event
	owner int64 // -1 = everything
}

// Manager owns all jobs.
type Manager struct {
	mu        sync.Mutex
	jobs      map[string]*Job
	subs      map[*subscriber]struct{}
	runner    *media.Runner
	sem       chan struct{}
	tempDir   string
	tempTTL   time.Duration
	retention time.Duration
	log       *slog.Logger

	// OnFinish is called (outside the lock) when a job reaches a terminal state.
	OnFinish func(j Job)
}

// NewManager creates a manager and cleans stale temp files.
func NewManager(runner *media.Runner, maxJobs int, tempDir string, tempTTL, retention time.Duration, log *slog.Logger) (*Manager, error) {
	dir := filepath.Join(tempDir, "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Files from a previous run are orphaned: nobody can download them.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return &Manager{
		jobs: map[string]*Job{}, subs: map[*subscriber]struct{}{}, runner: runner,
		sem: make(chan struct{}, maxJobs), tempDir: dir, tempTTL: tempTTL, retention: retention, log: log,
	}, nil
}

// snapshot copies a job for safe use outside the lock.
func (j *Job) snapshot() *Job {
	c := *j
	c.Prepare, c.cancel = nil, nil
	return &c
}

func (m *Manager) emit(ev Event) {
	for s := range m.subs {
		if s.owner >= 0 && s.owner != ev.owner {
			continue
		}
		select {
		case s.ch <- ev:
		default: // slow subscriber: drop the update, the next one supersedes it
		}
	}
}

// update mutates a job under the lock and notifies subscribers.
func (m *Manager) update(j *Job, fn func(j *Job)) {
	m.mu.Lock()
	prev := j.State
	fn(j)
	j.UpdatedAt = time.Now()
	// Throttle pure progress updates.
	if j.State == prev && !j.State.Terminal() && time.Since(j.lastNotify) < 400*time.Millisecond {
		m.mu.Unlock()
		return
	}
	j.lastNotify = time.Now()
	snap := j.snapshot()
	if !j.removed {
		m.emit(Event{Type: "job", Job: snap, owner: j.Owner})
	}
	finished := j.State.Terminal() && !prev.Terminal()
	m.mu.Unlock()
	if finished && m.OnFinish != nil {
		m.OnFinish(*snap)
	}
}

// Add registers a job. File mode jobs start immediately.
func (m *Manager) Add(j *Job) *Job {
	j.ID = auth.RandomHex(8)
	j.CreatedAt, j.UpdatedAt = time.Now(), time.Now()
	if j.Mode == ModeFile {
		j.State = StateQueued
	} else {
		j.State = StatePending
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	snap := j.snapshot()
	m.emit(Event{Type: "job", Job: snap, owner: j.Owner})
	m.mu.Unlock()
	if j.Mode == ModeFile {
		go m.runFile(j)
	}
	return snap
}

// Get returns a job visible to owner (-1 = any).
func (m *Manager) Get(id string, owner int64) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || owner >= 0 && j.Owner != owner {
		return nil, false
	}
	return j, true
}

// Snapshot returns a copy of a job visible to owner.
func (m *Manager) Snapshot(id string, owner int64) (*Job, bool) {
	j, ok := m.Get(id, owner)
	if !ok {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return j.snapshot(), true
}

// List returns jobs visible to owner, newest first.
func (m *Manager) List(owner int64) []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Job{}
	for _, j := range m.jobs {
		if owner < 0 || j.Owner == owner {
			out = append(out, j.snapshot())
		}
	}
	slices.SortFunc(out, func(a, b *Job) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out
}

// Subscribe returns a channel of events for owner (-1 = all).
func (m *Manager) Subscribe(owner int64) (<-chan Event, func()) {
	s := &subscriber{ch: make(chan Event, 64), owner: owner}
	m.mu.Lock()
	m.subs[s] = struct{}{}
	m.mu.Unlock()
	return s.ch, func() {
		m.mu.Lock()
		delete(m.subs, s)
		m.mu.Unlock()
	}
}

// Cancel stops a running job.
func (m *Manager) Cancel(id string, owner int64) error {
	j, ok := m.Get(id, owner)
	if !ok {
		return os.ErrNotExist
	}
	m.mu.Lock()
	cancel := j.cancel
	state := j.State
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !state.Terminal() && state != StateRunning {
		m.update(j, func(j *Job) { j.State = StateCanceled })
		m.removeTemp(j)
	}
	return nil
}

// Remove cancels and forgets a job.
func (m *Manager) Remove(id string, owner int64) error {
	if err := m.Cancel(id, owner); err != nil {
		return err
	}
	m.mu.Lock()
	j := m.jobs[id]
	if j == nil {
		m.mu.Unlock()
		return nil
	}
	j.removed = true
	delete(m.jobs, id)
	m.emit(Event{Type: "removed", ID: id, owner: j.Owner})
	m.mu.Unlock()
	m.removeTemp(j)
	return nil
}

func (m *Manager) removeTemp(j *Job) {
	m.mu.Lock()
	p := j.tempPath
	j.tempPath = ""
	m.mu.Unlock()
	if p != "" {
		os.Remove(p)
	}
}

func (m *Manager) acquire(ctx context.Context) error {
	select {
	case m.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) release() { <-m.sem }

func (m *Manager) progressFn(j *Job) func(media.Progress) {
	return func(p media.Progress) { m.update(j, func(j *Job) { j.Progress = p; j.Size = p.Size }) }
}

func (m *Manager) finishErr(ctx context.Context, j *Job, err error) {
	m.update(j, func(j *Job) {
		if ctx.Err() != nil {
			j.State = StateCanceled
			return
		}
		j.State, j.Error = StateFailed, err.Error()
	})
}

func (m *Manager) runFile(j *Job) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.mu.Lock()
	if j.State.Terminal() {
		m.mu.Unlock()
		return
	}
	j.cancel = cancel
	m.mu.Unlock()

	if err := m.acquire(ctx); err != nil {
		m.finishErr(ctx, j, err)
		return
	}
	defer m.release()
	m.update(j, func(j *Job) { j.State = StateRunning })

	plan, err := j.Prepare(ctx)
	if err != nil {
		m.finishErr(ctx, j, err)
		return
	}
	path := filepath.Join(m.tempDir, j.ID+"."+plan.Container.Ext)
	m.mu.Lock()
	j.tempPath = path
	m.mu.Unlock()
	if err := m.runner.Run(ctx, plan, j.Cookies, nil, path, m.progressFn(j)); err != nil {
		m.removeTemp(j)
		m.finishErr(ctx, j, err)
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		m.finishErr(ctx, j, err)
		return
	}
	m.update(j, func(j *Job) {
		j.State, j.Size, j.cancel = StateReady, st.Size(), nil
		j.Progress.Percent = 100
	})
}

// countingWriter counts bytes and flushes regularly so data keeps flowing.
type countingWriter struct {
	w       http.ResponseWriter
	n       int64
	flushed time.Time
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if time.Since(c.flushed) > time.Second {
		http.NewResponseController(c.w).Flush()
		c.flushed = time.Now()
	}
	return n, err
}

// Serve delivers a job to the browser: it streams stream-mode jobs and sends
// the prepared file of file-mode jobs.
func (m *Manager) Serve(w http.ResponseWriter, r *http.Request, j *Job) {
	if j.Mode == ModeFile {
		m.serveFile(w, r, j)
		return
	}
	m.serveStream(w, r, j)
}

func (m *Manager) serveStream(w http.ResponseWriter, r *http.Request, j *Job) {
	m.mu.Lock()
	if j.State == StateQueued || j.State == StateRunning {
		m.mu.Unlock()
		http.Error(w, "this download is already in progress", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	j.cancel = cancel
	m.mu.Unlock()

	m.update(j, func(j *Job) { j.State, j.Error, j.Progress, j.Size = StateQueued, "", media.Progress{}, 0 })
	if err := m.acquire(ctx); err != nil {
		m.finishErr(ctx, j, err)
		return
	}
	defer m.release()
	m.update(j, func(j *Job) { j.State = StateRunning })

	plan, err := j.Prepare(ctx)
	if err != nil {
		m.finishErr(ctx, j, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	setDownloadHeaders(w, j.Filename, plan.Container.Mime)
	cw := &countingWriter{w: w, flushed: time.Now()}
	err = m.runner.Run(ctx, plan, j.Cookies, cw, "", m.progressFn(j))
	if err != nil {
		m.finishErr(ctx, j, err)
		m.log.Warn("stream failed", "job", j.ID, "err", err)
		if cw.n == 0 {
			w.Header().Del("Content-Disposition")
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// Abort the connection so the browser marks the download as failed
		// instead of keeping a truncated file.
		panic(http.ErrAbortHandler)
	}
	m.update(j, func(j *Job) {
		j.State, j.Size, j.cancel = StateDone, cw.n, nil
		j.Progress.Percent = 100
	})
}

func (m *Manager) serveFile(w http.ResponseWriter, r *http.Request, j *Job) {
	m.mu.Lock()
	state, path := j.State, j.tempPath
	m.mu.Unlock()
	if state != StateReady && state != StateDone || path == "" {
		http.Error(w, "the file is not ready (state: "+string(state)+")", http.StatusConflict)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "the file is no longer available", http.StatusGone)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ct, _ := mimeForName(j.Filename)
	setDownloadHeaders(w, j.Filename, ct)
	if r.Header.Get("Range") != "" {
		// Resumed download: let ServeContent handle ranges; the TTL cleans up.
		http.ServeContent(w, r, j.Filename, st.ModTime(), f)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(st.Size()))
	w.Header().Set("Accept-Ranges", "bytes")
	n, err := io.Copy(w, f)
	if err == nil && n == st.Size() {
		m.removeTemp(j)
		m.update(j, func(j *Job) { j.State = StateDone })
	}
}

func mimeForName(name string) (string, bool) {
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	if c, ok := media.ContainerByID(ext); ok {
		return c.Mime, true
	}
	if t := mime.TypeByExtension("." + ext); t != "" {
		return t, true
	}
	return "application/octet-stream", false
}

func setDownloadHeaders(w http.ResponseWriter, filename, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", ContentDisposition(filename))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
}

// ContentDisposition builds an attachment header with a UTF-8 filename.
func ContentDisposition(filename string) string {
	ascii := strings.Map(func(r rune) rune {
		if r > 127 || r == '"' || r == '\\' || r < 32 {
			return '_'
		}
		return r
	}, filename)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, url.PathEscape(filename))
}

// SafeFilename turns a title into a portable file name (without extension).
func SafeFilename(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	s := strings.Join(nonEmpty, " - ")
	s = strings.Map(func(r rune) rune {
		switch {
		case strings.ContainsRune(`/\:*?"<>|`, r):
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, ". ")
	if r := []rune(s); len(r) > 150 {
		s = strings.TrimSpace(string(r[:150]))
	}
	if s == "" {
		s = "video"
	}
	return s
}

// Janitor expires temp files and forgets old jobs until ctx is done.
func (m *Manager) Janitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var expire, forget []*Job
		m.mu.Lock()
		for _, j := range m.jobs {
			age := time.Since(j.UpdatedAt)
			switch {
			case j.State == StateReady && age > m.tempTTL:
				expire = append(expire, j)
			case j.State.Terminal() && age > m.retention, j.State == StatePending && age > m.retention:
				forget = append(forget, j)
			}
		}
		m.mu.Unlock()
		for _, j := range expire {
			m.removeTemp(j)
			m.update(j, func(j *Job) { j.State = StateExpired })
			m.log.Info("temp file expired", "job", j.ID)
		}
		for _, j := range forget {
			_ = m.Remove(j.ID, -1)
		}
	}
}

// Shutdown cancels all running jobs and deletes temp files.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	var all []*Job
	for _, j := range m.jobs {
		all = append(all, j)
	}
	m.mu.Unlock()
	for _, j := range all {
		m.mu.Lock()
		c := j.cancel
		m.mu.Unlock()
		if c != nil {
			c()
		}
		m.removeTemp(j)
	}
}

// ErrNotVisible is returned for jobs of other users.
var ErrNotVisible = errors.New("job not found")
