// Package sourcemaps resolves minified stack frames back to original source
// positions using uploaded source maps. Symbolication is best-effort and runs
// on read: a frame is matched to its map by file basename, the generated
// position is mapped to the original source + line, and surrounding source
// context is attached. Any parse/lookup failure leaves the frame untouched.
package sourcemaps

import (
	"crypto/sha256"
	"encoding/json"
	"path"
	"strings"
	"sync"

	"github.com/go-sourcemap/sourcemap"
)

const contextLines = 5

// Frame is the (possibly symbolicated) stack-frame shape returned to the UI.
type Frame struct {
	Filename     string   `json:"filename,omitempty"`
	Function     string   `json:"function,omitempty"`
	Module       string   `json:"module,omitempty"`
	Lineno       int      `json:"lineno,omitempty"`
	Colno        int      `json:"colno,omitempty"`
	InApp        *bool    `json:"in_app,omitempty"`
	ContextLine  string   `json:"context_line,omitempty"`
	PreContext   []string `json:"pre_context,omitempty"`
	PostContext  []string `json:"post_context,omitempty"`
	Symbolicated bool     `json:"symbolicated,omitempty"`
}

// Resolver parses and caches source map consumers by content hash so repeated
// reads of the same map are cheap and concurrency-safe.
type Resolver struct {
	mu    sync.Mutex
	cache map[[32]byte]*sourcemap.Consumer // nil value = a cached parse failure
}

func NewResolver() *Resolver {
	return &Resolver{cache: make(map[[32]byte]*sourcemap.Consumer)}
}

func (r *Resolver) consumer(content string) *sourcemap.Consumer {
	key := sha256.Sum256([]byte(content))
	r.mu.Lock()
	if c, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return c
	}
	r.mu.Unlock()

	c, err := sourcemap.Parse("", []byte(content))
	if err != nil {
		c = nil
	}
	r.mu.Lock()
	r.cache[key] = c
	r.mu.Unlock()
	return c
}

// Symbolicate rewrites a raw stacktrace JSON ({"frames":[...]}) using the
// release's source maps (name -> .map content). Returns the original raw when
// there is nothing to do or anything fails, so a read never breaks on it.
func (r *Resolver) Symbolicate(raw json.RawMessage, maps map[string]string) json.RawMessage {
	if len(raw) == 0 || len(maps) == 0 {
		return raw
	}
	var st struct {
		Frames []Frame `json:"frames"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || len(st.Frames) == 0 {
		return raw
	}

	// Index by basename so a frame's "https://cdn/app.min.js" matches an
	// artifact uploaded as "app.min.js".
	byBase := make(map[string]string, len(maps))
	for name, content := range maps {
		byBase[path.Base(name)] = content
	}

	changed := false
	for i := range st.Frames {
		if r.symbolicateFrame(&st.Frames[i], maps, byBase) {
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(st)
	if err != nil {
		return raw
	}
	return out
}

func (r *Resolver) symbolicateFrame(f *Frame, exact, byBase map[string]string) bool {
	if f.Filename == "" || f.Lineno <= 0 {
		return false
	}
	content := exact[f.Filename]
	if content == "" {
		content = byBase[path.Base(f.Filename)]
	}
	if content == "" {
		return false
	}
	c := r.consumer(content)
	if c == nil {
		return false
	}

	// Source map columns are 0-based; browser stacks are often 1-based. Try the
	// column as-is, then one to the left, so a small off-by-one still resolves.
	src, name, line, col, ok := c.Source(f.Lineno, f.Colno)
	if !ok && f.Colno > 0 {
		src, name, line, col, ok = c.Source(f.Lineno, f.Colno-1)
	}
	if !ok || src == "" {
		return false
	}

	f.Filename = src
	if name != "" {
		f.Function = name
	}
	f.Lineno = line
	f.Colno = col
	f.Symbolicated = true
	if sc := c.SourceContent(src); sc != "" {
		f.ContextLine, f.PreContext, f.PostContext = contextOf(sc, line)
	}
	return true
}

// contextOf returns the source line at lineno (1-based) plus up to contextLines
// before and after.
func contextOf(source string, lineno int) (ctx string, pre, post []string) {
	lines := strings.Split(source, "\n")
	idx := lineno - 1
	if idx < 0 || idx >= len(lines) {
		return "", nil, nil
	}
	ctx = lines[idx]
	for i := idx - contextLines; i < idx; i++ {
		if i >= 0 {
			pre = append(pre, lines[i])
		}
	}
	for i := idx + 1; i <= idx+contextLines && i < len(lines); i++ {
		post = append(post, lines[i])
	}
	return ctx, pre, post
}
