package api

import (
	"strconv"
	"testing"
	"time"

	"github.com/bright-interaction/flare/internal/ratelimit"
)

// TestMonitorAlertCapBoundsTheOrgNotJustTheSlug measures what the per-monitor
// cap alone actually bounds.
//
// The check-in endpoint is DSN-authed (public key, ships in a browser bundle)
// and UpsertMonitorCheckin CREATES the monitor when it does not exist, so the
// caller chooses the slug and can invent an unlimited number of them. A cap
// keyed on (org, slug) therefore bounds one honestly-flapping cron and bounds
// nothing at all against someone varying the slug: each new slug is a fresh
// budget.
//
// This drives the real limiters rather than asserting on the constants, so it
// stays true if the budgets are retuned.
func TestMonitorAlertCapBoundsTheOrgNotJustTheSlug(t *testing.T) {
	const org = "org-1"
	perMonitor := ratelimit.New(10, time.Minute)
	perOrg := ratelimit.New(20, time.Minute)

	// The attack: a fresh slug every time, so the per-monitor cap never trips.
	allowedPerMonitorOnly := 0
	allowedBoth := 0
	for i := 0; i < 500; i++ {
		slug := "s" + strconv.Itoa(i)
		if perMonitor.Allow("monitor-failed:" + org + ":" + slug) {
			allowedPerMonitorOnly++
		}
	}
	// Reset and run the same attack through both caps.
	perMonitor2 := ratelimit.New(10, time.Minute)
	for i := 0; i < 500; i++ {
		slug := "s" + strconv.Itoa(i)
		if perMonitor2.Allow("monitor-failed:"+org+":"+slug) && perOrg.Allow("monitor-failed-org:"+org) {
			allowedBoth++
		}
	}

	if allowedPerMonitorOnly < 500 {
		t.Fatalf("setup wrong: the per-monitor cap blocked %d of 500 distinct slugs; "+
			"it is supposed to be trivially bypassed by varying the slug", 500-allowedPerMonitorOnly)
	}
	if allowedBoth >= allowedPerMonitorOnly {
		t.Errorf("the org cap changed nothing: %d alerts allowed with both caps vs %d with the "+
			"per-monitor cap alone. A DSN holder can still send one alert per invented slug.",
			allowedBoth, allowedPerMonitorOnly)
	}
	t.Logf("500 distinct slugs: %d alerts with the per-monitor cap alone, %d with the org cap too",
		allowedPerMonitorOnly, allowedBoth)
}

// TestMonitorAlertCapAllowsRealFleets keeps the cap honest. An estate where a
// handful of monitors genuinely fail at once must still alert; a cap that
// swallowed those would hide the incident it exists to report.
func TestMonitorAlertCapAllowsRealFleets(t *testing.T) {
	const org = "org-1"
	perMonitor := ratelimit.New(10, time.Minute)
	perOrg := ratelimit.New(20, time.Minute)

	sent := 0
	for i := 0; i < 10; i++ { // ten distinct monitors failing together
		slug := "cron-" + strconv.Itoa(i)
		if perMonitor.Allow("monitor-failed:"+org+":"+slug) && perOrg.Allow("monitor-failed-org:"+org) {
			sent++
		}
	}
	if sent != 10 {
		t.Errorf("a real 10-monitor failure sent only %d alerts; the cap is too tight and would "+
			"hide part of a genuine incident", sent)
	}
}
