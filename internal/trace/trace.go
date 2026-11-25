// Package trace records investigations as JSON Lines and serves recorded tool
// results back, so an investigation can be re-run without access to the
// systems it originally queried.
//
// A trace file holds one header record, then one record per tool call, then
// optionally one report record:
//
//	{"type":"header","version":1,"alert":{...},"tools":[...]}
//	{"type":"call","id":"c1","tool":"k8s__events_list","args":{...},"result":{...}}
//	{"type":"report","report":{...}}
package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/hindcast/hindcast/internal/tool"
)

// Version is the trace format version written by this build.
const Version = 1

// Header opens every trace.
type Header struct {
	Version   int             `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
	Alert     json.RawMessage `json:"alert"`
	Tools     []tool.Spec     `json:"tools"`
}

// Call is one tool call and what it returned. Error is set when the call
// failed outside the tool (see tool.Box).
type Call struct {
	ID         string          `json:"id"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	Prefetch   bool            `json:"prefetch,omitempty"` // made before the model's first turn
	Result     tool.Result     `json:"result"`
	Error      string          `json:"error,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
	DurationMS int64           `json:"duration_ms"`
}

type record struct {
	Type    string          `json:"type"`
	Version int             `json:"version,omitempty"`
	Report  json.RawMessage `json:"report,omitempty"`
	*Header
	*Call
}

// Trace is a fully read trace file.
type Trace struct {
	Header Header
	Calls  []Call
	Report json.RawMessage
}

// Writer appends records to a trace. It is safe for concurrent use.
type Writer struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

// NewWriter returns a Writer that writes to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// WriteHeader writes the header record. Call it once, first.
func (t *Writer) WriteHeader(h Header) error {
	return t.write(record{Type: "header", Version: Version, Header: &h})
}

// WriteCall writes one call record.
func (t *Writer) WriteCall(c Call) error {
	return t.write(record{Type: "call", Call: &c})
}

// WriteReport writes the final report record.
func (t *Writer) WriteReport(report any) error {
	data, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return t.write(record{Type: "report", Report: data})
}

func (t *Writer) write(r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode trace record: %w", err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	if _, err := t.w.Write(append(data, '\n')); err != nil {
		t.err = fmt.Errorf("write trace: %w", err)
	}
	return t.err
}

// ReadFile reads a trace from disk.
func ReadFile(path string) (*Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr, err := Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return tr, nil
}

// Read parses a trace.
func Read(r io.Reader) (*Trace, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	var tr Trace
	sawHeader := false
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		switch rec.Type {
		case "header":
			if sawHeader {
				return nil, fmt.Errorf("line %d: second header", line)
			}
			if rec.Header == nil || rec.Version != Version {
				return nil, fmt.Errorf("line %d: unsupported trace version %d", line, rec.Version)
			}
			tr.Header = *rec.Header
			tr.Header.Version = rec.Version
			sawHeader = true
		case "call":
			if !sawHeader {
				return nil, fmt.Errorf("line %d: call before header", line)
			}
			tr.Calls = append(tr.Calls, *rec.Call)
		case "report":
			tr.Report = rec.Report
		default:
			return nil, fmt.Errorf("line %d: unknown record type %q", line, rec.Type)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawHeader {
		return nil, errors.New("trace has no header")
	}
	return &tr, nil
}
