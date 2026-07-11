package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
)

// EnvelopeItem is one item from a Sentry envelope: its type ("event",
// "transaction", "session", ...) and raw JSON payload. Callers dispatch on Type
// (event -> error path, transaction -> spans).
type EnvelopeItem struct {
	Type    string
	Payload []byte
}

// ParseEnvelope splits a Sentry envelope into its items. An envelope is
// newline-delimited: an envelope header, then alternating item header / item
// payload lines. Every JSON item is returned with its Type; the caller decides
// what to do with each (error path vs spans; sessions/attachments ignored).
//
// Limitation: length-prefixed binary items are not supported; event and
// transaction payloads from the official SDKs are single-line JSON, which this
// handles.
func ParseEnvelope(body []byte) ([]EnvelopeItem, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var lines [][]byte
	for sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}

	var items []EnvelopeItem
	// lines[0] is the envelope header. Items are header/payload pairs from index 1.
	for i := 1; i < len(lines); i += 2 {
		var hdr struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(lines[i], &hdr); err != nil {
			break
		}
		if i+1 >= len(lines) {
			break
		}
		items = append(items, EnvelopeItem{Type: hdr.Type, Payload: lines[i+1]})
	}
	return items, nil
}
