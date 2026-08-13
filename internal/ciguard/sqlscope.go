package ciguard

import "strings"

// GUARD SOUNDNESS -- scope-scan body reduction (ported from brightcrm, 2026-08-04).
//
// orgPredicate is a regex run over the WHOLE query body, so a match anywhere
// satisfies the guard. That is weaker than it looks. The 2026-07-26 rewrite
// already fixed "org_id is merely mentioned" by requiring the column to meet a
// bound parameter, and it strips `--` line comments before matching. Three
// regions survive both of those and still let a token pass that proves nothing
// about which rows the caller may read:
//
//   - the SELECT projection. A scalar sub-select carries its own WHERE, so
//     `SELECT (SELECT count(*) FROM issues WHERE org_id = $1) AS n FROM
//     releases` matches while the OUTER query reads every org's releases. This
//     is the exact shape of the ListReleasesByProject bug the guard exists to
//     catch, one level up.
//   - a JOIN ... ON condition. `FROM issues i LEFT JOIN orgs o ON i.org_id =
//     $1` matches, and on a LEFT JOIN the condition does not restrict the left
//     table at all: every issue row is still returned.
//   - block comments. reLineComment handles `--` only, so a `/* org_id = $1 */`
//     note satisfies a security guard with prose.
//
// scopeScanBody blanks those regions before the predicate is applied. String
// literals are deliberately NOT blanked: sqlc spells a bound argument as
// `sqlc.arg('org_id')`, inside quotes.
//
// Blanked bytes become spaces rather than being deleted, so offsets stay
// aligned with the original body and a failure message can still be read.

// sqlTok is one lexical token of a SQL body: lower-cased text, byte range in
// the source, the parenthesis nesting depth it sits at, and whether it is a
// bare word (keyword or identifier) rather than punctuation.
type sqlTok struct {
	text   string
	start  int
	end    int
	depth  int
	isWord bool
}

func isSQLWordByte(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// lexSQL tokenizes a SQL body and additionally returns the byte ranges of every
// comment. String and quoted-identifier literals are consumed without emitting
// a token, so a keyword spelled inside one cannot steer the scan.
func lexSQL(src string) (toks []sqlTok, comments [][2]int) {
	depth := 0
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			start := i
			for i < len(src) && src[i] != '\n' {
				i++
			}
			comments = append(comments, [2]int{start, i})
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			start := i
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			if i+1 < len(src) {
				i += 2
			} else {
				i = len(src)
			}
			comments = append(comments, [2]int{start, i})
		case c == '\'' || c == '"':
			q := c
			i++
			for i < len(src) {
				if src[i] == q {
					// A doubled quote is an escaped quote, not the terminator.
					if i+1 < len(src) && src[i+1] == q {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '(':
			toks = append(toks, sqlTok{text: "(", start: i, end: i + 1, depth: depth})
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			toks = append(toks, sqlTok{text: ")", start: i, end: i + 1, depth: depth})
			i++
		case isSQLWordByte(c):
			j := i
			for j < len(src) && isSQLWordByte(src[j]) {
				j++
			}
			toks = append(toks, sqlTok{
				text: strings.ToLower(src[i:j]), start: i, end: j, depth: depth, isWord: true,
			})
			i = j
		default:
			toks = append(toks, sqlTok{text: src[i : i+1], start: i, end: i + 1, depth: depth})
			i++
		}
	}
	return toks, comments
}

// onClauseEnd are the keywords that terminate a JOIN ... ON condition at the
// same paren depth. `on` is included so a second join's ON ends the first.
var onClauseEnd = map[string]bool{
	"where": true, "join": true, "inner": true, "left": true, "right": true,
	"full": true, "cross": true, "natural": true, "lateral": true, "group": true,
	"order": true, "limit": true, "offset": true, "having": true, "union": true,
	"intersect": true, "except": true, "window": true, "for": true,
	"returning": true, "on": true,
}

// joinOnSearchEnd are the keywords that mean a JOIN had no ON condition (a
// CROSS JOIN, or a USING(...) join), so the ON search must stop.
var joinOnSearchEnd = map[string]bool{
	"where": true, "join": true, "using": true, "group": true, "order": true,
	"limit": true, "offset": true, "having": true, "union": true,
	"intersect": true, "except": true, "returning": true,
}

// scopeScanBody returns body with the regions in which a tenant-scoping token
// is not a row-isolation predicate replaced by spaces. See the file header.
func scopeScanBody(body string) string {
	toks, comments := lexSQL(body)
	out := []byte(body)
	blank := func(from, to int) {
		if from < 0 {
			from = 0
		}
		if to > len(out) {
			to = len(out)
		}
		for k := from; k < to; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	for _, c := range comments {
		blank(c[0], c[1])
	}

	// levelEnd returns the byte offset where the paren level of toks[at]
	// closes, i.e. the start of the first later token shallower than depth.
	levelEnd := func(at, depth int) int {
		for j := at; j < len(toks); j++ {
			if toks[j].depth < depth {
				return toks[j].start
			}
		}
		return len(body)
	}

	// blankProjection blanks [from,to) except the spans occupied by nested
	// SELECT statements. A scalar sub-select in the projection is its own
	// statement with its own WHERE, so `(SELECT count(*) FROM issues i WHERE
	// i.org_id = $1) AS n` really is scoped and must not be blanked away.
	// The main loop reaches that nested SELECT afterwards and blanks ITS
	// projection, so the reduction is applied at every level.
	blankProjection := func(selIdx, from, to int) {
		d := toks[selIdx].depth
		pos := from
		for j := selIdx + 1; j < len(toks) && toks[j].start < to; j++ {
			if !toks[j].isWord || toks[j].text != "select" || toks[j].depth <= d {
				continue
			}
			blank(pos, toks[j].start)
			pos = levelEnd(j+1, toks[j].depth)
			for j+1 < len(toks) && toks[j+1].start < pos {
				j++
			}
		}
		blank(pos, to)
	}

	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if !t.isWord {
			continue
		}
		switch t.text {
		case "select":
			// Blank up to this SELECT's own FROM. A SELECT with no FROM at its
			// own depth (a scalar `(SELECT 1)`) is blanked to the end of its
			// paren level.
			end := levelEnd(i+1, t.depth)
			for j := i + 1; j < len(toks); j++ {
				if toks[j].depth < t.depth {
					break
				}
				if toks[j].depth == t.depth && toks[j].isWord && toks[j].text == "from" {
					end = toks[j].start
					break
				}
			}
			blankProjection(i, t.end, end)

		case "returning":
			blank(t.end, levelEnd(i+1, t.depth))

		case "join":
			// Locate this join's ON, then blank the condition it introduces.
			onIdx := -1
			for j := i + 1; j < len(toks); j++ {
				if toks[j].depth < t.depth {
					break
				}
				if toks[j].depth != t.depth || !toks[j].isWord {
					continue
				}
				if toks[j].text == "on" {
					onIdx = j
					break
				}
				if joinOnSearchEnd[toks[j].text] {
					break
				}
			}
			if onIdx < 0 {
				continue
			}
			end := levelEnd(onIdx+1, toks[onIdx].depth)
			for j := onIdx + 1; j < len(toks); j++ {
				if toks[j].depth < toks[onIdx].depth {
					break
				}
				if toks[j].depth == toks[onIdx].depth && toks[j].isWord && onClauseEnd[toks[j].text] {
					end = toks[j].start
					break
				}
			}
			blank(toks[onIdx].start, end)
		}
	}
	return string(out)
}
