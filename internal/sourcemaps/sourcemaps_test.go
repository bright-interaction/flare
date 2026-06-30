package sourcemaps

import (
	"encoding/json"
	"testing"
)

// A minimal valid source map: generated position (line 1, col 0) maps to
// original orig.js line 1 col 0 ("AAAA"), with inline sourcesContent.
const miniMap = `{"version":3,"sources":["orig.js"],"sourcesContent":["const x = 1;\nthrow new Error('boom');\nconst y = 2;"],"names":[],"mappings":"AAAA"}`

func TestSymbolicateResolvesFrame(t *testing.T) {
	raw := json.RawMessage(`{"frames":[{"filename":"app.min.js","function":"c","lineno":1,"colno":0}]}`)
	out := NewResolver().Symbolicate(raw, map[string]string{"app.min.js": miniMap})

	var st struct {
		Frames []Frame `json:"frames"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	f := st.Frames[0]
	if !f.Symbolicated {
		t.Fatalf("frame not symbolicated: %+v", f)
	}
	if f.Filename != "orig.js" {
		t.Errorf("filename = %q, want orig.js", f.Filename)
	}
	if f.Lineno != 1 {
		t.Errorf("lineno = %d, want 1", f.Lineno)
	}
	if f.ContextLine != "const x = 1;" {
		t.Errorf("context_line = %q, want %q", f.ContextLine, "const x = 1;")
	}
}

func TestSymbolicateNoMapIsNoop(t *testing.T) {
	raw := json.RawMessage(`{"frames":[{"filename":"app.min.js","lineno":1,"colno":5}]}`)
	out := NewResolver().Symbolicate(raw, nil)
	if string(out) != string(raw) {
		t.Errorf("expected raw passthrough with no maps, got %s", out)
	}
}

func TestSymbolicateUnmatchedFilePassesThrough(t *testing.T) {
	raw := json.RawMessage(`{"frames":[{"filename":"other.js","lineno":1,"colno":0}]}`)
	out := NewResolver().Symbolicate(raw, map[string]string{"app.min.js": miniMap})
	if string(out) != string(raw) {
		t.Errorf("unmatched file should pass through unchanged, got %s", out)
	}
}
