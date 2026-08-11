package ai

// Payment-card recognition, shared by the scrubber (Scrub) and the ingest-time
// sensitive-data detector (api.detectSensitive) so the two can never drift.
//
// A valid Luhn check digit is NOT evidence that a number is a payment card.
// Luhn is a single mod-10 check digit, so one in ten arbitrary digit runs
// passes it by chance. Gating only on Luhn therefore destroyed 10% of EVERY
// 13-19 digit value that crossed the scrubber, measured exactly at 10.0% over
// 10,000 sequential ids.
//
// That was not theoretical. Flare's own alerting rewrote
//
//	/var/backups/hephaestus/state-20260810-175840.sqlite
//
// to "state-[card].sqlite" (the timestamp 20260810175840 is Luhn-valid) and
// raised a false "sensitive-data-in-payload" security event for it, so the
// operator lost the one field naming which backup run had failed and got
// paged about a payment-card leak that never happened.
//
// The issuer prefix is what makes this a card detector rather than a
// decimation of long numbers. Requiring a real IIN plus a length that issuer
// actually mints takes the false-positive rate on ordinary ids from 10% to
// ~1%, and to ZERO for date-derived runs, which all begin 19xx/20xx and match
// no issuer.

// cardRange is one issuer's numbering rule: an inclusive numeric prefix range
// at a given prefix width, plus the PAN lengths that issuer actually mints.
type cardRange struct {
	lo, hi  int
	digits  int // number of leading digits lo/hi are compared against
	lengths []int
}

// Published IIN ranges for the networks that actually appear in leaked
// payloads. Maestro's 12-13 digit range is deliberately excluded: it is loose
// enough to reintroduce the false positives this table exists to remove, and a
// 12-digit Maestro PAN is vanishingly rare outside legacy POS traffic.
var cardRanges = []cardRange{
	{4, 4, 1, []int{13, 16, 19}},       // Visa
	{51, 55, 2, []int{16}},             // Mastercard
	{2221, 2720, 4, []int{16}},         // Mastercard (2-series)
	{34, 34, 2, []int{15}},             // American Express
	{37, 37, 2, []int{15}},             // American Express
	{6011, 6011, 4, []int{16, 19}},     // Discover
	{644, 649, 3, []int{16, 19}},       // Discover
	{65, 65, 2, []int{16, 19}},         // Discover
	{300, 305, 3, []int{14, 16, 19}},   // Diners Club
	{36, 36, 2, []int{14, 16, 19}},     // Diners Club
	{38, 39, 2, []int{14, 16, 19}},     // Diners Club
	{3528, 3589, 4, []int{16, 19}},     // JCB
	{62, 62, 2, []int{16, 17, 18, 19}}, // UnionPay
	{81, 81, 2, []int{16, 17, 18, 19}}, // RuPay
}

// IsPaymentCard reports whether a digits-only string is plausibly a real
// payment card: a known issuer prefix, a PAN length that issuer mints, AND a
// valid Luhn check digit. All three are required. See the file comment for why
// Luhn alone is not enough.
func IsPaymentCard(digits string) bool {
	if len(digits) < 12 || len(digits) > 19 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	if !matchesIssuer(digits) {
		return false
	}
	return luhn(digits)
}

// matchesIssuer reports whether digits opens with a published IIN whose issuer
// mints a PAN of exactly this length.
func matchesIssuer(digits string) bool {
	for _, cr := range cardRanges {
		if len(digits) < cr.digits {
			continue
		}
		prefix := 0
		for i := 0; i < cr.digits; i++ {
			prefix = prefix*10 + int(digits[i]-'0')
		}
		if prefix < cr.lo || prefix > cr.hi {
			continue
		}
		for _, n := range cr.lengths {
			if len(digits) == n {
				return true
			}
		}
	}
	return false
}
