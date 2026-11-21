// Package logging provides the two log streams of the application:
//
//   - the application log (structured slog records: startup, checks, alerts,
//     errors), and
//   - the HTTP access log (one line per request, Apache "combined" or JSON).
//
// Each stream can be written to stdout, stderr, a file or nowhere. Files are
// opened in append mode and can be reopened (SIGHUP) after an external tool
// such as logrotate has moved them away.
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

// Destination names accepted besides a file path.
const (
	Stdout = "stdout"
	Stderr = "stderr"
	Off    = "off"
)

// Output is a log destination. Writes are serialised, so one Output can be
// shared by several loggers without interleaving lines.
type Output struct {
	mu   sync.Mutex
	name string    // stdout, stderr, off or the file path
	path string    // non-empty for files
	w    io.Writer // current writer
	f    *os.File  // open file, if path != ""
}

// Name describes the destination for log messages.
func (o *Output) Name() string { return o.name }

// Enabled reports whether the output writes anywhere.
func (o *Output) Enabled() bool { return o.name != Off }

// IsFile reports whether the output is a file.
func (o *Output) IsFile() bool { return o.path != "" }

func (o *Output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.w.Write(p)
}

// reopen closes and reopens the file so that rotated logs are released.
func (o *Output) reopen() error {
	if o.path == "" {
		return nil
	}
	f, err := openFile(o.path)
	if err != nil {
		return err
	}
	o.mu.Lock()
	old := o.f
	o.f, o.w = f, f
	o.mu.Unlock()
	return old.Close()
}

func (o *Output) close() error {
	if o.f == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.w = io.Discard
	err := o.f.Close()
	o.f = nil
	return err
}

func openFile(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("log directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return f, nil
}

// Outputs opens log destinations. Destinations that name the same file share
// one Output, so the application and access logs may point to the same file.
type Outputs struct {
	mu    sync.Mutex
	files map[string]*Output
	std   map[string]*Output
}

// NewOutputs returns an empty set of outputs.
func NewOutputs() *Outputs {
	return &Outputs{files: map[string]*Output{}, std: map[string]*Output{}}
}

// Open returns the output for dest: "stdout", "stderr", "off" (also "none",
// "-" is stdout) or a file path. An empty dest yields def.
func (s *Outputs) Open(dest, def string) (*Output, error) {
	name := strings.TrimSpace(dest)
	if name == "" {
		name = def
	}
	switch strings.ToLower(name) {
	case Stdout, "-":
		return s.std1(Stdout, os.Stdout), nil
	case Stderr:
		return s.std1(Stderr, os.Stderr), nil
	case Off, "none", "false", "disabled":
		return s.std1(Off, io.Discard), nil
	}
	path, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.files[path]; ok {
		return o, nil
	}
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	o := &Output{name: path, path: path, w: f, f: f}
	s.files[path] = o
	return o, nil
}

func (s *Outputs) std1(name string, w io.Writer) *Output {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.std[name]; ok {
		return o
	}
	o := &Output{name: name, w: w}
	s.std[name] = o
	return o
}

// Reopen reopens every file output (log rotation).
func (s *Outputs) Reopen() error {
	s.mu.Lock()
	files := make([]*Output, 0, len(s.files))
	for _, o := range s.files {
		files = append(files, o)
	}
	s.mu.Unlock()
	var errs []error
	for _, o := range files {
		if err := o.reopen(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close closes every file output.
func (s *Outputs) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, o := range s.files {
		if err := o.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// NewAppLogger creates the application logger. level is debug, info, warn
// or error; format is text or json.
func NewAppLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.TrimSpace(level))); err != nil {
		return nil, fmt.Errorf("invalid log level %q (want debug, info, warn or error)", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("invalid log format %q (want text or json)", format)
}
