package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	in := "user jane@corp.com from 10.1.2.3 with token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 at app.js:42 in handler()"
	out := Scrub(in)
	for _, leaked := range []string{"jane@corp.com", "10.1.2.3", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ"} {
		if strings.Contains(out, leaked) {
			t.Errorf("PII leaked: %q still in %q", leaked, out)
		}
	}
	// code structure must survive.
	if !strings.Contains(out, "app.js:42") || !strings.Contains(out, "handler()") {
		t.Errorf("code structure scrubbed away: %q", out)
	}
}

func TestScrubHardened(t *testing.T) {
	// Each input carries a secret/PII value that must NOT survive scrubbing.
	leaks := map[string]string{
		"formatted card (spaces)": "card 4111 1111 1111 1111 declined",
		"formatted card (dashes)": "pan=4111-1111-1111-1111",
		"aws secret (assignment)": "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"db url password":         "postgres://svc:s3cr3tPass@db.internal:5432/app",
		"generic password":        `{"password":"hunter2horse"}`,
		"bearer token":            "Authorization: Bearer abcDEF123456ghiJKL",
		"github oauth token":      "using gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
		"google api key":          "key AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"pem private key":         "-----BEGIN RSA PRIVATE KEY-----\nMIIEabc123\n-----END RSA PRIVATE KEY-----",
	}
	secrets := map[string]string{
		"formatted card (spaces)": "4111 1111 1111 1111",
		"formatted card (dashes)": "4111-1111-1111-1111",
		"aws secret (assignment)": "wJalrXUtnFEMI",
		"db url password":         "s3cr3tPass",
		"generic password":        "hunter2horse",
		"bearer token":            "abcDEF123456ghiJKL",
		"github oauth token":      "gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"google api key":          "AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"pem private key":         "MIIEabc123",
	}
	for name, in := range leaks {
		out := Scrub(in)
		if strings.Contains(out, secrets[name]) {
			t.Errorf("%s: secret leaked through scrubber: %q -> %q", name, in, out)
		}
	}

	// Ordinary code and a non-card long id must survive intact.
	if got := Scrub("order 1234567890123456 failed in checkout() at pay.go:88"); !strings.Contains(got, "checkout()") || !strings.Contains(got, "pay.go:88") {
		t.Errorf("code structure scrubbed away: %q", got)
	}
	// A Luhn-invalid 16-digit run must not be labelled a card.
	if got := Scrub("id 1234567890123456"); strings.Contains(got, "[card]") {
		t.Errorf("non-card number mislabelled as card: %q", got)
	}
}

func TestCompleteOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("openai path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing bearer auth")
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		if body["model"] != "m" {
			t.Errorf("model not sent")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"root cause: X"}}]}`))
	}))
	defer srv.Close()

	got, err := New(false).Complete(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m", Format: "openai"}, "sys", "usr")
	if err != nil {
		t.Fatal(err)
	}
	if got != "root cause: X" {
		t.Errorf("got %q", got)
	}
}

// TestNoPIIOnWire is the sovereignty guarantee: a realistic, PII-laden issue
// context, once scrubbed, must reach the model endpoint with zero raw personal
// data or credentials in the outbound request body. Proven over the actual wire
// for both request shapes.
func TestNoPIIOnWire(t *testing.T) {
	rawContext := `Error: failed to charge customer jane.doe@corp.com
Level: error
Release: web@1.4.2
Exception: payment declined for card 4539123412341234
Request from 203.0.113.42 (device a1:b2:c3:d4:e5:f6)
Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJqYW5lIn0.s3cr3tSignaturePart
Stripe key sk_live_REDACTED used
Session hash 5f4dcc3b5aa765d61d8327deb882cf99aa1234567890abcd
  at chargeCustomer (billing.js:88)
  at handleCheckout (checkout.js:214)`

	leaks := []string{
		"jane.doe@corp.com", "4539123412341234", "203.0.113.42",
		"a1:b2:c3:d4:e5:f6", "eyJhbGciOiJIUzI1NiJ9", "sk_live_REDACTED",
		"5f4dcc3b5aa765d61d8327deb882cf99aa1234567890abcd",
	}

	for _, format := range []string{"openai", "anthropic"} {
		t.Run(format, func(t *testing.T) {
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				if format == "anthropic" {
					w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
				} else {
					w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
				}
			}))
			defer srv.Close()

			scrubbed := Scrub(rawContext)
			if _, err := New(false).Complete(context.Background(),
				Config{BaseURL: srv.URL, APIKey: "k", Model: "m", Format: format}, "sys", scrubbed); err != nil {
				t.Fatal(err)
			}

			for _, leak := range leaks {
				if strings.Contains(gotBody, leak) {
					t.Errorf("PII %q reached the model endpoint over the wire", leak)
				}
			}
			// Code structure must still be on the wire so triage stays useful.
			for _, keep := range []string{"billing.js:88", "chargeCustomer", "checkout.js:214"} {
				if !strings.Contains(gotBody, keep) {
					t.Errorf("code structure %q lost before reaching the model", keep)
				}
			}
		})
	}
}

func TestCompleteAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("anthropic path = %q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing anthropic headers")
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"the fix is Y"}]}`))
	}))
	defer srv.Close()

	got, err := New(false).Complete(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m", Format: "anthropic"}, "sys", "usr")
	if err != nil {
		t.Fatal(err)
	}
	if got != "the fix is Y" {
		t.Errorf("got %q", got)
	}
}
