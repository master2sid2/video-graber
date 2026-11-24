package jobs

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/master2sid2/video-graber/internal/media"
)

type memStore struct {
	mu   sync.Mutex
	recs map[string]Record
}

func (s *memStore) SaveJob(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[r.ID] = r
	return nil
}

func (s *memStore) DeleteJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recs, id)
	return nil
}

func (s *memStore) LoadJobs() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, r := range s.recs {
		out = append(out, r)
	}
	return out, nil
}

func newManager(t *testing.T, st Persister) *Manager {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := NewManager(&media.Runner{Log: log}, 1, t.TempDir(), time.Hour, 24*time.Hour, log)
	if err != nil {
		t.Fatal(err)
	}
	m.Store = st
	return m
}

func TestQueueSurvivesRestart(t *testing.T) {
	st := &memStore{recs: map[string]Record{}}
	m := newManager(t, st)
	prep := func(context.Context) (*media.Plan, error) { return nil, context.Canceled }
	active := m.Add(&Job{Title: "Active", Mode: ModeStream, Owner: 7, HistoryID: 42, Cookies: "secret", Prepare: prep})
	done := m.Add(&Job{Title: "Done", Mode: ModeStream, Owner: 7, Prepare: prep})
	j, _ := m.Get(done.ID, -1)
	m.update(j, func(j *Job) { j.State, j.Size = StateDone, 123 })
	gone := m.Add(&Job{Title: "Removed", Mode: ModeStream, Prepare: prep})
	if err := m.Remove(gone.ID, -1); err != nil {
		t.Fatal(err)
	}
	for _, r := range st.recs {
		if strings.Contains(string(r.Data), "secret") {
			t.Fatal("cookies must never be persisted")
		}
	}

	// "Restart": a fresh manager loads the same store.
	m2 := newManager(t, st)
	var finished []Job
	m2.OnFinish = func(j Job) { finished = append(finished, j) }
	if err := m2.Restore(); err != nil {
		t.Fatal(err)
	}
	if len(m2.List(-1)) != 2 {
		t.Fatalf("restored %d jobs, want 2", len(m2.List(-1)))
	}
	a, ok := m2.Snapshot(active.ID, 7)
	if !ok || a.State != StateFailed || a.Error == "" || a.Restartable {
		t.Fatalf("interrupted job: %+v", a)
	}
	d, _ := m2.Snapshot(done.ID, 7)
	if d.State != StateDone || d.Size != 123 {
		t.Fatalf("finished job: %+v", d)
	}
	if len(finished) != 1 || finished[0].HistoryID != 42 {
		t.Fatalf("history must be updated for interrupted jobs: %+v", finished)
	}
}
