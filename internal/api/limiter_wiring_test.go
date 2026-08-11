package api

import (
	"reflect"
	"testing"

	"github.com/bright-interaction/flare/internal/config"
	"github.com/bright-interaction/flare/internal/ratelimit"
)

// Every rate-limit gate test in this package (register, accept-invite,
// reset-password) builds its own &Server{} literal carrying the one limiter it
// needs. That is what lets them run without Postgres, and it is also a hole
// none of them can see: they prove the HANDLER consults a limiter, and say
// nothing about whether NewServer ever created one.
//
// Ablate the wiring line for any of those limiters and the whole package still
// passes, while production nil-derefs on the first request to that route and
// serves a 500 through Recoverer. A green suite standing in for the real thing
// is exactly the substitution those gates exist to prevent, so the gates should
// not be the place it survives.
//
// This closes it for the class rather than for one field. It walks the real
// Server built by the real NewServer and asserts every *ratelimit.Limiter on it
// is non-nil, by REFLECTION rather than by a hand-kept list, so a limiter added
// later is covered on the commit that adds it instead of on the commit where
// someone remembers this file. The gate tests keep their literals; this is the
// one test that holds the constructor to them.
func TestNewServerWiresEveryLimiter(t *testing.T) {
	// NewServer is pure construction: no dialing, no goroutines. pgxpool.New
	// does not connect eagerly (see deadPool), a nil session manager and nil
	// analytics manager are only stored, and secretbox.New tolerates an empty
	// key by design. So this needs no infrastructure.
	srv := NewServer(deadPool(t), nil, config.Config{}, nil)

	limiterType := reflect.TypeOf((*ratelimit.Limiter)(nil))
	v := reflect.ValueOf(srv).Elem()
	st := v.Type()

	var found, unwired []string
	for i := 0; i < st.NumField(); i++ {
		if st.Field(i).Type != limiterType {
			continue
		}
		found = append(found, st.Field(i).Name)
		// IsNil on an unexported field is allowed; Interface() would not be.
		if v.Field(i).IsNil() {
			unwired = append(unwired, st.Field(i).Name)
		}
	}

	// Without this the test passes loudly for the wrong reason: rename the type
	// or move the limiters behind a struct and the loop matches nothing, which
	// reads identically to "everything is wired".
	if len(found) == 0 {
		t.Fatal("no *ratelimit.Limiter fields found on Server: this test is no longer checking anything")
	}
	if len(unwired) > 0 {
		t.Errorf("NewServer left %d of %d limiters nil: %v.\n"+
			"Each one nil-derefs on the first request to its route. The per-route gate "+
			"tests cannot catch this: they build their own Server literal.",
			len(unwired), len(found), unwired)
	}
	t.Logf("checked %d limiter fields: %v", len(found), found)
}
