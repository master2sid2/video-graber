// Package logging builds the application and access loggers.
//
// Each logger writes to a configurable set of outputs: stdout, stderr and any
// number of files. Files are reopened on Reopen (wired to SIGHUP) so that
// external log rotation works.
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// reopenFile is an append-only file that can be reopened in place.
type reopenFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

func openReopenFile(path string) (*reopenFile, error) {
	r := &reopenFile{path: path}
	if err := r.Reopen(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *reopenFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Write(p)
}

func (r *reopenFile) Reopen() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	r.mu.Lock()
	old := r.f
	r.f = f
	r.mu.Unlock()
	if old != nil {
		return old.Close()
	}
	return nil
}

func (r *reopenFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// lockedWriter serialises writes coming from several goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Sink is a set of outputs a logger writes to.
type Sink struct {
	io.Writer
	files []*reopenFile
}

// Enabled reports whether the sink has at least one output.
func (s *Sink) Enabled() bool { return s.Writer != io.Discard }

// Reopen reopens all file outputs.
func (s *Sink) Reopen() error {
	var errs []error
	for _, f := range s.files {
		errs = append(errs, f.Reopen())
	}
	return errors.Join(errs...)
}

// Close closes all file outputs.
func (s *Sink) Close() error {
	var errs []error
	for _, f := range s.files {
		errs = append(errs, f.Close())
	}
	return errors.Join(errs...)
}

// OpenSink parses a comma separated output spec such as
// "stdout,/var/log/vg/app.log" or "none".
func OpenSink(spec string) (*Sink, error) {
	s := &Sink{}
	var writers []io.Writer
	for part := range strings.SplitSeq(spec, ",") {
		part = strings.TrimSpace(part)
		switch strings.ToLower(part) {
		case "":
			continue
		case "none", "off", "discard":
			continue
		case "stdout", "-":
			writers = append(writers, os.Stdout)
		case "stderr":
			writers = append(writers, os.Stderr)
		default:
			path, _ := strings.CutPrefix(part, "file:")
			f, err := openReopenFile(path)
			if err != nil {
				s.Close()
				return nil, fmt.Errorf("open log file %q: %w", path, err)
			}
			s.files = append(s.files, f)
			writers = append(writers, f)
		}
	}
	switch len(writers) {
	case 0:
		s.Writer = io.Discard
	case 1:
		s.Writer = &lockedWriter{w: writers[0]}
	default:
		s.Writer = &lockedWriter{w: io.MultiWriter(writers...)}
	}
	return s, nil
}

// ParseLevel converts a textual level to slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(s))
	return l, err
}

// NewAppLogger creates the application logger writing to sink.
func NewAppLogger(sink *Sink, level, format string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level %q", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(format) {
	case "json":
		return slog.New(slog.NewJSONHandler(sink, opts)), nil
	case "text", "":
		return slog.New(slog.NewTextHandler(sink, opts)), nil
	default:
		return nil, fmt.Errorf("invalid log format %q: want text or json", format)
	}
}
