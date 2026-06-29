package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
)

// ParseEnvelope extracts the "event" item payloads from a Sentry envelope.
// An envelope is newline-delimited: an envelope header, then alternating item
// header / item payload lines. We read event items only (the error path);
// other item types (sessions, attachments) are skipped.
//
// Limitation: length-prefixed binary items are not supported; event payloads
// from the official SDKs are single-line JSON, which this handles.
func ParseEnvelope(body []byte) ([][]byte, error) {
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

	var events [][]byte
	// lines[0] is the envelope header. Items start at index 1.
	for i := 1; i+1 < len(lines) || i < len(lines); i += 2 {
		if i >= len(lines) {
			break
		}
		var hdr struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(lines[i], &hdr); err != nil {
			break
		}
		if i+1 >= len(lines) {
			break
		}
		payload := lines[i+1]
		if hdr.Type == "event" {
			events = append(events, payload)
		}
	}
	return events, nil
}
