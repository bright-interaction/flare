// Package ingest parses incoming error payloads (Sentry-wire compatible) into
// Flare's normalized event shape and computes the grouping fingerprint. It is
// pure: no database, no HTTP. The api layer handles transport and persistence.
package ingest

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

// Frame is one stack frame, a subset of the Sentry frame interface.
type Frame struct {
	Filename    string `json:"filename,omitempty"`
	Function    string `json:"function,omitempty"`
	Module      string `json:"module,omitempty"`
	Lineno      int    `json:"lineno,omitempty"`
	Colno       int    `json:"colno,omitempty"`
	InApp       *bool  `json:"in_app,omitempty"`
	ContextLine string `json:"context_line,omitempty"`
}

func (f Frame) inApp() bool { return f.InApp == nil || *f.InApp }

// NormalizedEvent is the internal representation persisted to the events table.
type NormalizedEvent struct {
	EventID        string
	Level          string
	Platform       string
	Environment    string
	Release        string
	Title          string
	Culprit        string
	ExceptionType  string
	ExceptionValue string
	Message        string
	Frames         []Frame
	Raw            json.RawMessage
}

// sentryEvent is the subset of the Sentry event payload Flare reads.
type sentryEvent struct {
	EventID     string          `json:"event_id"`
	Level       string          `json:"level"`
	Platform    string          `json:"platform"`
	Environment string          `json:"environment"`
	Release     string          `json:"release"`
	Transaction string          `json:"transaction"`
	Message     json.RawMessage `json:"message"`
	Logentry    *struct {
		Formatted string `json:"formatted"`
		Message   string `json:"message"`
	} `json:"logentry"`
	Exception *struct {
		Values []struct {
			Type       string `json:"type"`
			Value      string `json:"value"`
			Stacktrace *struct {
				Frames []Frame `json:"frames"`
			} `json:"stacktrace"`
		} `json:"values"`
	} `json:"exception"`
}

// ParseEvent decodes a single event payload and normalizes it.
func ParseEvent(raw []byte) (NormalizedEvent, error) {
	var se sentryEvent
	if err := json.Unmarshal(raw, &se); err != nil {
		return NormalizedEvent{}, err
	}

	ev := NormalizedEvent{
		EventID:     se.EventID,
		Level:       firstNonEmpty(se.Level, "error"),
		Platform:    se.Platform,
		Environment: se.Environment,
		Release:     se.Release,
		Culprit:     se.Transaction,
		Message:     messageText(se),
		Raw:         json.RawMessage(raw),
	}

	if se.Exception != nil && len(se.Exception.Values) > 0 {
		// Sentry orders values oldest-first; the last is the outermost.
		last := se.Exception.Values[len(se.Exception.Values)-1]
		ev.ExceptionType = last.Type
		ev.ExceptionValue = last.Value
		if last.Stacktrace != nil {
			ev.Frames = last.Stacktrace.Frames
		}
	}

	ev.Title = title(ev)
	if ev.Culprit == "" {
		ev.Culprit = culprit(ev.Frames)
	}
	return ev, nil
}

// Fingerprint is the stable grouping key. Exceptions group by type + in-app
// frame signatures; bare messages group by their text.
func (e NormalizedEvent) Fingerprint() string {
	h := sha1.New()
	if e.ExceptionType != "" {
		io.WriteString(h, e.ExceptionType)
		wrote := false
		for _, f := range e.Frames {
			if f.inApp() && (f.Function != "" || f.Module != "") {
				io.WriteString(h, "\x1f"+f.Module+":"+f.Function)
				wrote = true
			}
		}
		if !wrote {
			io.WriteString(h, "\x1f"+e.ExceptionValue)
		}
	} else {
		io.WriteString(h, strings.TrimSpace(e.Message))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func title(e NormalizedEvent) string {
	if e.ExceptionType != "" {
		if e.ExceptionValue != "" {
			return e.ExceptionType + ": " + e.ExceptionValue
		}
		return e.ExceptionType
	}
	if e.Message != "" {
		return truncate(e.Message, 200)
	}
	return "Error"
}

func culprit(frames []Frame) string {
	// Topmost in-app frame, else topmost frame.
	for i := len(frames) - 1; i >= 0; i-- {
		f := frames[i]
		if f.inApp() && (f.Function != "" || f.Module != "") {
			return strings.TrimPrefix(f.Module+"/"+f.Function, "/")
		}
	}
	if len(frames) > 0 {
		f := frames[len(frames)-1]
		return strings.TrimPrefix(f.Module+"/"+f.Function, "/")
	}
	return ""
}

func messageText(se sentryEvent) string {
	if se.Logentry != nil {
		if se.Logentry.Formatted != "" {
			return se.Logentry.Formatted
		}
		if se.Logentry.Message != "" {
			return se.Logentry.Message
		}
	}
	if len(se.Message) == 0 {
		return ""
	}
	// message may be a bare string or {message, formatted}.
	var s string
	if json.Unmarshal(se.Message, &s) == nil {
		return s
	}
	var obj struct {
		Formatted string `json:"formatted"`
		Message   string `json:"message"`
	}
	if json.Unmarshal(se.Message, &obj) == nil {
		return firstNonEmpty(obj.Formatted, obj.Message)
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
