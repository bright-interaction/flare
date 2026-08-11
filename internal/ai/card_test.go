package ai

import (
	"fmt"
	"strings"
	"testing"
)

// Public, non-functional test PANs published by the card networks. Every one
// must still be detected: the fix narrows false positives, it must not narrow
// real detection.
var realTestCards = []struct {
	network string
	pan     string
}{
	{"visa-16", "4111111111111111"},
	{"visa-16-alt", "4012888888881881"},
	{"visa-13", "4222222222222"},
	{"mastercard", "5555555555554444"},
	{"mastercard-alt", "5105105105105100"},
	{"mastercard-2series", "2223003122003222"},
	{"amex", "378282246310005"},
	{"amex-alt", "371449635398431"},
	{"discover", "6011111111111117"},
	{"discover-alt", "6011000990139424"},
	{"jcb", "3530111333300000"},
	{"jcb-alt", "3566002020360505"},
	{"diners-14", "30569309025904"},
	{"diners-14-alt", "38520000023237"},
	{"unionpay", "6250941006528599"},
}

// Values that are Luhn-VALID but are not cards. Every one of these was
// destroyed as "[card]" before the issuer check existed. They are the whole
// point of the fix, so they are listed explicitly rather than generated.
var luhnValidNonCards = []struct {
	name  string
	value string
}{
	// The exact production string, from the 2026-08-10 security event.
	{"hephaestus backup timestamp", "20260810175840"},
	{"another backup timestamp", "20260731055840"},
	{"luhn-valid order id", "1234567890123452"},
	{"luhn-valid 14-digit date", "20250102120000"},
	{"luhn-valid 14-digit date alt", "20250104055840"},
	{"epoch nanoseconds", "1712345678901234506"},
}

func TestRealCardsStillDetected(t *testing.T) {
	for _, c := range realTestCards {
		if !IsPaymentCard(c.pan) {
			t.Errorf("%s: %s is a real test PAN and must be detected", c.network, c.pan)
		}
		if !strings.Contains(Scrub("charge "+c.pan+" declined"), "[card]") {
			t.Errorf("%s: Scrub must replace %s", c.network, c.pan)
		}
	}
}

func TestLuhnValidNonCardsSurvive(t *testing.T) {
	for _, c := range luhnValidNonCards {
		if !luhn(c.value) {
			t.Fatalf("%s: test vector %q is not Luhn-valid, so it does not exercise the bug this test exists for", c.name, c.value)
		}
		if IsPaymentCard(c.value) {
			t.Errorf("%s: %s passes Luhn but is not a card and must not be flagged", c.name, c.value)
		}
		if got := Scrub(c.value); strings.Contains(got, "[card]") {
			t.Errorf("%s: Scrub(%s) = %s, must not be redacted as a card", c.name, c.value, got)
		}
	}
}

// TestBackupTimestampRegression reproduces the production failure end to end:
// the operator must be able to read which backup run failed.
func TestBackupTimestampRegression(t *testing.T) {
	const msg = "hephaestus backup failed at db_snapshot: backup: vacuum into " +
		"/var/backups/hephaestus/state-20260810-175840.sqlite: unable to open database file"
	got := Scrub(msg)
	if !strings.Contains(got, "20260810-175840") {
		t.Errorf("the timestamp naming the failed run was destroyed:\n got: %s", got)
	}
}

// TestFalsePositiveRateOnOrdinaryIDs measures the property rather than
// asserting it from the code. Luhn alone passes one run in ten; that was the
// real, measured false-positive rate. With the issuer check the rate over the
// same population must be zero, because sequential ids of these widths open
// with no published IIN.
func TestFalsePositiveRateOnOrdinaryIDs(t *testing.T) {
	for _, width := range []int{13, 14, 16, 19} {
		luhnPassed, flagged := 0, 0
		for i := 0; i < 10000; i++ {
			s := fmt.Sprintf("%0*d", width, i)
			if luhn(s) {
				luhnPassed++
			}
			if IsPaymentCard(s) {
				flagged++
			}
		}
		// Sanity: the population really does trip Luhn ~10% of the time, so a
		// zero below means the issuer check worked and not that the sample was
		// inert.
		if luhnPassed < 800 || luhnPassed > 1200 {
			t.Fatalf("width %d: %d/10000 passed Luhn, expected ~1000; the sample is not exercising the bug", width, luhnPassed)
		}
		if flagged != 0 {
			t.Errorf("width %d: %d/10000 zero-padded ids flagged as cards, want 0 (Luhn alone flagged %d)", width, flagged, luhnPassed)
		}
	}
}

// TestIssuerCheckIsLoadBearing is the ablation. If IsPaymentCard is ever
// weakened back to a bare Luhn check, the vectors above stop proving anything,
// so this asserts the issuer gate is what rejects them and not the length
// bounds or the digit filter.
func TestIssuerCheckIsLoadBearing(t *testing.T) {
	for _, c := range luhnValidNonCards {
		if matchesIssuer(c.value) {
			t.Errorf("%s: %s matches a published IIN, so it is rejected by something other than the issuer check and is a weak test vector", c.name, c.value)
		}
	}
	// And the converse: every real PAN must be accepted BY the issuer check,
	// otherwise the table is passing them for some unrelated reason.
	for _, c := range realTestCards {
		if !matchesIssuer(c.pan) {
			t.Errorf("%s: %s must match a published IIN", c.network, c.pan)
		}
	}
}
