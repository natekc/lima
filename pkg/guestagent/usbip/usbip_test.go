// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package usbip

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunner records every invocation and lets each test wire up a
// canned response (stdout + error). Stored as a slice so multi-step
// flows (e.g. detach: first `usbip port`, then `usbip detach`) can
// assert the exact command sequence.
type fakeRunner struct {
	responses []runResponse
	calls     [][]string
}

type runResponse struct {
	out string
	err error
}

func (f *fakeRunner) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(f.calls) > len(f.responses) {
		return "", errors.New("fakeRunner: more calls than responses")
	}
	r := f.responses[len(f.calls)-1]
	return r.out, r.err
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestShellAttach(t *testing.T) {
	t.Run("server with port splits into --tcp-port + bare host", func(t *testing.T) {
		f := &fakeRunner{responses: []runResponse{{}}}
		s := &Shell{Run: f.run}
		if err := s.Attach(context.Background(), "host:3240", "1-2"); err != nil {
			t.Fatalf("Attach: %v", err)
		}
		want := []string{"--tcp-port", "3240", "attach", "-r", "host", "-b", "1-2"}
		if len(f.calls) != 1 || !equalArgs(f.calls[0], want) {
			t.Fatalf("unexpected calls: %v", f.calls)
		}
	})

	t.Run("bare host passes through without --tcp-port", func(t *testing.T) {
		f := &fakeRunner{responses: []runResponse{{}}}
		s := &Shell{Run: f.run}
		if err := s.Attach(context.Background(), "host", "1-2"); err != nil {
			t.Fatalf("Attach: %v", err)
		}
		want := []string{"attach", "-r", "host", "-b", "1-2"}
		if len(f.calls) != 1 || !equalArgs(f.calls[0], want) {
			t.Fatalf("unexpected calls: %v", f.calls)
		}
	})

	t.Run("already-attached returns the sentinel", func(t *testing.T) {
		// Callers (the reconciler) use errors.Is to distinguish
		// "we attached this" from "this was already attached by
		// someone else" so they don't claim ownership of a foreign
		// attachment.
		f := &fakeRunner{responses: []runResponse{
			{out: "usbip: error: already used by a different user", err: errors.New("exit 1")},
		}}
		s := &Shell{Run: f.run}
		err := s.Attach(context.Background(), "host:3240", "1-2")
		if !errors.Is(err, ErrAlreadyAttached) {
			t.Fatalf("expected ErrAlreadyAttached, got %v", err)
		}
	})

	t.Run("other errors propagate with command context", func(t *testing.T) {
		f := &fakeRunner{responses: []runResponse{
			{out: "usbip: error: no remote device", err: errors.New("exit 1")},
		}}
		s := &Shell{Run: f.run}
		err := s.Attach(context.Background(), "host:3240", "1-2")
		if err == nil {
			t.Fatal("expected error")
		}
		// Error should mention the busid so log lines are actionable.
		if got := err.Error(); !contains(got, "1-2") || !contains(got, "host:3240") {
			t.Fatalf("error missing context: %s", got)
		}
	})

	t.Run("unrelated error containing 'already' is not swallowed", func(t *testing.T) {
		// Regression: a loose substring match on "already" would
		// silently treat any future error containing that word as
		// success. Make sure only the known "already attached" /
		// "already used" / "already in use" / "device already bound"
		// phrases are recognised.
		f := &fakeRunner{responses: []runResponse{
			{out: "usbip: error: device already removed from sysfs", err: errors.New("exit 1")},
		}}
		s := &Shell{Run: f.run}
		if err := s.Attach(context.Background(), "host:3240", "1-2"); err == nil {
			t.Fatal("expected error to propagate for unrelated 'already' message")
		}
	})

	t.Run("kernel-driver-bind 'already bound' is not swallowed", func(t *testing.T) {
		// "already bound" is generic kernel-driver vocabulary —
		// after S3 it must only be recognised when prefixed with
		// "device " so a future or distro-patched usbip build that
		// emits e.g. "driver foo already bound to interface bar"
		// surfaces as a real error rather than a silent success.
		f := &fakeRunner{responses: []runResponse{
			{out: "usbip: error: driver foo already bound to interface bar", err: errors.New("exit 1")},
		}}
		s := &Shell{Run: f.run}
		err := s.Attach(context.Background(), "host:3240", "1-2")
		if err == nil || errors.Is(err, ErrAlreadyAttached) {
			t.Fatalf("expected driver-bind message to propagate, got %v", err)
		}
	})

	t.Run("empty server or busid is rejected without calling usbip", func(t *testing.T) {
		f := &fakeRunner{}
		s := &Shell{Run: f.run}
		if err := s.Attach(context.Background(), "", "1-2"); err == nil {
			t.Fatal("expected error for empty server")
		}
		if err := s.Attach(context.Background(), "host", ""); err == nil {
			t.Fatal("expected error for empty busid")
		}
		if len(f.calls) != 0 {
			t.Fatalf("usbip should not be invoked, got %v", f.calls)
		}
	})
}

const samplePortOutput = `Imported USB devices
====================
Port 00: <Port in Use> at Full Speed(12Mbps)
       Yubico : YubiKey OTP+FIDO+CCID (1050:0407)
       4-1 -> usbip://192.168.5.2:3240/1-1
           -> remote bus/dev 001/002

Port 01: <Port in Use> at High Speed(480Mbps)
       SanDisk : Cruzer (0781:5530)
       4-2 -> usbip://other:9999/2-3
           -> remote bus/dev 001/003
`

func TestParseUsbipPort(t *testing.T) {
	cases := []struct {
		name     string
		server   string
		busid    string
		wantPort string
	}{
		{"first device matches port 00", "192.168.5.2:3240", "1-1", "00"},
		{"second device matches port 01", "other:9999", "2-3", "01"},
		{"unknown busid returns empty (no error)", "192.168.5.2:3240", "9-9", ""},
		{"wrong server with right busid does not match", "wronghost:3240", "1-1", ""},
		{"empty input returns empty", "any", "any", ""},
		// Some `usbip-utils` builds elide the default :3240 from the
		// `usbip port` URL. The configured server still has it, so a
		// literal compare would miss the match; hostsMatch defaults
		// both sides to 3240 to keep detach working.
		{"URL without default port matches host:3240", "192.168.5.2:3240", "1-1", "00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := samplePortOutput
			if tc.name == "empty input returns empty" {
				in = ""
			}
			if tc.name == "URL without default port matches host:3240" {
				// Re-render the sample with the port elided from
				// the URL (as some usbip-utils builds do for default
				// ports), without changing any other detail.
				in = strings.ReplaceAll(samplePortOutput, "usbip://192.168.5.2:3240/", "usbip://192.168.5.2/")
			}
			got := parseUsbipPort(in, tc.server, tc.busid)
			if got != tc.wantPort {
				t.Fatalf("got port %q want %q", got, tc.wantPort)
			}
		})
	}
}

func TestShellDetach(t *testing.T) {
	t.Run("happy path: usbip port then usbip detach -p PORT", func(t *testing.T) {
		f := &fakeRunner{responses: []runResponse{
			{out: samplePortOutput},
			{},
		}}
		s := &Shell{Run: f.run}
		if err := s.Detach(context.Background(), "192.168.5.2:3240", "1-1"); err != nil {
			t.Fatalf("Detach: %v", err)
		}
		if len(f.calls) != 2 {
			t.Fatalf("expected 2 calls, got %v", f.calls)
		}
		if !equalArgs(f.calls[0], []string{"port"}) {
			t.Fatalf("first call: %v", f.calls[0])
		}
		if !equalArgs(f.calls[1], []string{"detach", "-p", "00"}) {
			t.Fatalf("second call: %v", f.calls[1])
		}
	})

	t.Run("not-attached is a no-op, no detach invoked", func(t *testing.T) {
		f := &fakeRunner{responses: []runResponse{
			{out: samplePortOutput},
		}}
		s := &Shell{Run: f.run}
		if err := s.Detach(context.Background(), "host", "missing"); err != nil {
			t.Fatalf("Detach: %v", err)
		}
		if len(f.calls) != 1 {
			t.Fatalf("expected only `usbip port` call, got %v", f.calls)
		}
	})

	t.Run("usbip port failure is reported with context", func(t *testing.T) {
		f := &fakeRunner{responses: []runResponse{
			{out: "no vhci_hcd kernel module", err: errors.New("exit 1")},
		}}
		s := &Shell{Run: f.run}
		err := s.Detach(context.Background(), "host", "1-1")
		if err == nil || !contains(err.Error(), "usbip port") {
			t.Fatalf("expected wrapped error, got %v", err)
		}
	})
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSplitServer(t *testing.T) {
	cases := []struct {
		in, host, port string
	}{
		{"host", "host", ""},
		{"host.lima.internal:3240", "host.lima.internal", "3240"},
		{"192.168.5.2:9999", "192.168.5.2", "9999"},
		{"[::1]:3240", "::1", "3240"},
		{"", "", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			h, p := splitServer(c.in)
			if h != c.host || p != c.port {
				t.Fatalf("splitServer(%q) = (%q, %q); want (%q, %q)", c.in, h, p, c.host, c.port)
			}
		})
	}
}

func TestHostsMatch(t *testing.T) {
	cases := []struct {
		name  string
		a, b  string
		match bool
	}{
		{"identical", "host:3240", "host:3240", true},
		{"port-default-elided", "host", "host:3240", true},
		{"different host", "a:3240", "b:3240", false},
		{"different port", "host:3240", "host:3241", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostsMatch(tc.a, tc.b); got != tc.match {
				t.Fatalf("hostsMatch(%q,%q)=%v want %v", tc.a, tc.b, got, tc.match)
			}
		})
	}
}

func TestWantedBusids_ReportsUnresolved(t *testing.T) {
	exports := []RemoteDevice{{BusID: "1-1", VendorID: "0525", ProductID: "a4a0"}}
	devs := []Device{
		{BusID: "1-1"},                        // resolves
		{BusID: "9-9"},                        // missing
		{VendorID: "0525", ProductID: "a4a0"}, // resolves
		{VendorID: "dead", ProductID: "beef"}, // missing
	}
	want, unresolved := wantedBusids(devs, exports)
	if _, ok := want["1-1"]; !ok || len(want) != 1 {
		t.Fatalf("want = %v; expected exactly {1-1}", want)
	}
	if len(unresolved) != 2 || unresolved[0] != "9-9" || unresolved[1] != "dead:beef" {
		t.Fatalf("unresolved = %v; expected [9-9 dead:beef]", unresolved)
	}
}
