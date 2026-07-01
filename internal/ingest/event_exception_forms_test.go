package ingest

import "testing"

func TestParseEventExceptionObjectForm(t *testing.T) {
	raw := []byte(`{"event_id":"a1","exception":{"values":[{"type":"TypeError","value":"x is not a function"}]}}`)
	ev, err := ParseEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ExceptionType != "TypeError" {
		t.Fatalf("object form: ExceptionType = %q", ev.ExceptionType)
	}
}

func TestParseEventExceptionArrayForm(t *testing.T) {
	// sentry-go serializes exception as a bare array, not {"values":[...]}.
	raw := []byte(`{"event_id":"a2","platform":"go","exception":[{"type":"*errors.errorString","value":"boom","stacktrace":{"frames":[{"function":"main.main","lineno":10}]}}]}`)
	ev, err := ParseEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ExceptionType != "*errors.errorString" || ev.ExceptionValue != "boom" {
		t.Fatalf("array form: got type=%q value=%q", ev.ExceptionType, ev.ExceptionValue)
	}
	if len(ev.Frames) != 1 || ev.Frames[0].Function != "main.main" {
		t.Fatalf("array form: frames not parsed: %+v", ev.Frames)
	}
}
