package scan

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
)

// JSON scrubs a JSON document by walking it and rewriting its leaves, then
// re-marshalling.
//
// Never run Text over serialized JSON directly: the number rules
// (\b\d{13,19}\b -> [number] and the card rule) rewrite UNQUOTED numeric
// values, so {"duration_ns":1712345678901234567} became
// {"duration_ns":[number]}, which is not valid JSON. Callers wrap the result in
// json.RawMessage, and encoding/json then fails on the whole response; the MCP
// layer's marshal fallback rendered the []byte as a decimal byte dump.
// Nanosecond timestamps are 19 digits, so this fired on ordinary OTLP traffic.
//
// The return value is ALWAYS valid JSON, including for input that was not JSON
// to begin with: callers embed it as a json.RawMessage, so returning anything
// else would reintroduce the same failure by another route.
func JSON(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Numbers stay as their literal text. Decoding into float64 silently rounds
	// every integer above 2^53, so a 19-digit id or nanosecond timestamp would
	// come back to the operator as 1.7123456789012346e+18.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		// Not JSON, or JSON followed by trailing content. Decode alone stops at
		// the end of the first value and silently discarded everything after
		// it, so a blob whose second half held the secret came back "clean".
		return unparsed(raw)
	}
	out, err := marshalJSON(scrubValue(v))
	if err != nil {
		return []byte(`{"_scrub_error":"redacted"}`)
	}
	return out
}

// unparsed wraps text that is not a single JSON document in an OBJECT rather
// than returning a bare JSON string. The API contract for stacktrace and
// attributes is object-or-array, and quietly swapping in a different JSON type
// on the error path means a consumer that indexes the field sees a type error
// exactly when the data was already unusual.
func unparsed(raw []byte) []byte {
	out, err := marshalJSON(map[string]string{"_unparsed": Text(string(raw))})
	if err != nil {
		return []byte(`{"_scrub_error":"redacted"}`)
	}
	return out
}

// scrubValue rewrites leaves. Both string values AND object keys are rewritten:
// a header map serialises the credential name into the key half often enough
// (`{"sk_live_...":"seen"}`) that leaving keys alone was a real egress path.
//
// Key order is not preserved (encoding/json emits map keys alphabetically).
// That is cosmetic for the model reading it, but worth knowing before anyone
// diffs a stack trace across the scrub.
func scrubValue(v any) any {
	switch t := v.(type) {
	case string:
		return Text(t)
	case json.Number:
		return scrubNumber(t)
	case map[string]any:
		return scrubObject(t)
	case []any:
		for i := range t {
			t[i] = scrubValue(t[i])
		}
		return t
	default:
		return v
	}
}

func scrubObject(t map[string]any) map[string]any {
	// Deterministic order so a key collision resolves the same way every run.
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]any, len(t))
	for _, k := range keys {
		// The VALUE is scrubbed according to what the key calls it, so a
		// "release" attribute holding a git SHA is not rewritten to "[hash]".
		val := t[k]
		if s, ok := val.(string); ok {
			val = Field(k, s)
		} else {
			val = scrubValue(val)
		}
		// Two distinct keys can scrub to one string. Suffix rather than
		// overwrite: dropping a value silently is worse than an odd key.
		nk := Text(k)
		if _, dup := out[nk]; dup {
			for i := 2; ; i++ {
				cand := nk + "#" + strconv.Itoa(i)
				if _, taken := out[cand]; !taken {
					nk = cand
					break
				}
			}
		}
		out[nk] = val
	}
	return out
}

// scrubNumber applies the CONTENT rules to a numeric leaf and leaves the
// FORM rules alone.
//
// A payment card sent as {"pan":4111111111111111} is the same leak as
// {"pan":"4111111111111111"}, and the string arm caught it while the numeric
// arm did not: same value, different JSON type, opposite security outcome.
// OTLP attributes are typed, so an integer attribute is ordinary traffic.
//
// The generic long-number rule deliberately does NOT run here. Applying it
// would rewrite every nanosecond timestamp and every 13-19 digit id to
// "[number]", which is the exact telemetry-destroying bug the type-preserving
// walk was written to fix.
func scrubNumber(n json.Number) any {
	lit := n.String()
	d := onlyDigits(lit)
	if IsPaymentCard(d) {
		return "[card]"
	}
	if len(d) == 12 && luhn(d[2:]) && personnummerShape(lit) {
		return "[personnummer]"
	}
	return n
}

// personnummerShape reports whether a bare 12-digit literal has a real
// birth date in it. The Luhn check is applied by the caller; both are needed,
// exactly as in the text rule.
func personnummerShape(lit string) bool {
	return rePersonnummer.MatchString(lit)
}

// marshalJSON encodes without HTML-escaping so <, > and & inside a stack trace
// or a log body are not mangled into < on their way to the model.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
