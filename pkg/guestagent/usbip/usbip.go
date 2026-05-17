// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

// Package usbip wraps the Linux usbip(8) userspace utility so that the
// guestagent can attach and detach USB/IP devices.
//
// The package has three layers:
//
//   - The [Attacher] interface defines the two write operations the
//     reconciler needs. The [Inspector] interface (in reconciler.go)
//     defines the two read operations. Both interfaces let the
//     reconciler be unit-tested without a kernel.
//   - [Shell] is the production implementation that shells out to
//     `usbip` and parses its output.
//   - [Reconciler] (in reconciler.go) polls each configured USB/IP
//     server and converges the kernel's attachment state toward the
//     declared device list. This is what gives Lima hotplug support
//     without requiring any server-specific event protocol.
package usbip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
)

// Attacher is the minimal contract the guestagent uses to drive the
// kernel's USB/IP stack. Both methods are idempotent in spirit:
// attaching a device that is already attached, or detaching one that
// is already gone, must not return an error — the hostagent may
// re-deliver an event after a reconnect.
// ErrAlreadyAttached is returned (wrapped) from [Shell.Attach] when
// the device was already attached prior to the call — typically by a
// previous lima session whose in-memory ownership was lost across a
// guestagent restart, or by the user out of band. Callers use
// [errors.Is] to distinguish "we just attached this" from "this was
// already attached" so they do not claim ownership of a foreign
// attachment.
var ErrAlreadyAttached = errors.New("usbip: device already attached")

type Attacher interface {
	// Attach imports the (server, busid) device into the guest. The
	// device shows up as a local USB device once the call returns.
	Attach(ctx context.Context, server, busid string) error

	// Detach releases a previously-attached device. If the device is
	// not currently attached the call is a no-op.
	Detach(ctx context.Context, server, busid string) error
}

// Shell is the production [Attacher] that runs the `usbip` command.
//
// The zero value is ready to use; in tests, set [Shell.Run] to swap
// the underlying command runner so the unit tests don't need root or
// the kernel modules. The [Shell.Run] field defaults to running
// `usbip` with the given args.
type Shell struct {
	// Run executes `usbip ARGS...` and returns combined stdout/stderr.
	// Tests substitute a fake; production callers leave it nil to use
	// the real command.
	Run func(ctx context.Context, args ...string) (string, error)
}

func (s *Shell) run(ctx context.Context, args ...string) (string, error) {
	if s.Run != nil {
		return s.Run(ctx, args...)
	}
	out, err := exec.CommandContext(ctx, "usbip", args...).CombinedOutput()
	return string(out), err
}

// splitServer separates an opaque `host[:port]` server string into
// the bare host (suitable for `usbip ... -r`) and an optional decimal
// port (suitable for `usbip --tcp-port`). The `-r` flag of usbip(8)
// accepts only a hostname; the port must be conveyed via the
// top-level `--tcp-port` flag, which is why we cannot just pass the
// joined form straight through.
func splitServer(server string) (host, port string) {
	if h, p, err := net.SplitHostPort(server); err == nil {
		return h, p
	}
	return server, ""
}

// withTCPPort prepends `--tcp-port PORT` to args when port is set.
// The flag is top-level (must precede the subcommand) per `usbip
// --help`.
func withTCPPort(port string, args ...string) []string {
	if port == "" {
		return args
	}
	full := make([]string, 0, len(args)+2)
	full = append(full, "--tcp-port", port)
	return append(full, args...)
}

// defaultUsbipPort is the IANA-assigned and de-facto USB/IP port. It
// is the default the rest of Lima fills in when a server is given as a
// bare host, and the value some `usbip-utils` builds elide from the
// `usbip port` URL output. Centralising it keeps the host-compare
// logic in [hostsMatch] in sync with that default.
const defaultUsbipPort = "3240"

// hostsMatch compares two `host[:port]` strings, treating a missing
// port on either side as the default [defaultUsbipPort]. This lets
// the URL-rendered form `usbip://host/` (which some `usbip-utils`
// builds emit when the port is the default) match the configured
// `host:3240` form.
func hostsMatch(a, b string) bool {
	ah, ap := splitServer(a)
	bh, bp := splitServer(b)
	if ap == "" {
		ap = defaultUsbipPort
	}
	if bp == "" {
		bp = defaultUsbipPort
	}
	return ah == bh && ap == bp
}

// Attach calls `usbip [--tcp-port PORT] attach -r HOST -b BUSID`.
// Returns [ErrAlreadyAttached] (which callers should check with
// [errors.Is]) when `usbip attach` exits non-zero with a recognisable
// "already attached" message — this distinguishes "we just attached
// this" from "this was already attached by someone else" so callers
// don't claim ownership of a foreign attachment.
func (s *Shell) Attach(ctx context.Context, server, busid string) error {
	if strings.TrimSpace(server) == "" || strings.TrimSpace(busid) == "" {
		return fmt.Errorf("usbip attach: server and busid must be non-empty (server=%q busid=%q)", server, busid)
	}
	host, port := splitServer(server)
	args := withTCPPort(port, "attach", "-r", host, "-b", busid)
	out, err := s.run(ctx, args...)
	if err != nil {
		if isAlreadyAttached(out) {
			logrus.Debugf("usbip attach: device %s @ %s is already attached", busid, server)
			return ErrAlreadyAttached
		}
		return fmt.Errorf("usbip attach -r %s -b %s: %w (%s)", server, busid, err, strings.TrimSpace(out))
	}
	return nil
}

// Detach finds the local vhci_hcd port associated with `(server, busid)`
// by parsing `usbip port`, then runs `usbip detach -p PORT`. If the
// device is not currently attached the call returns nil.
func (s *Shell) Detach(ctx context.Context, server, busid string) error {
	port, err := s.findLocalPort(ctx, server, busid)
	if err != nil {
		return err
	}
	if port == "" {
		logrus.Debugf("usbip detach: %s @ %s not attached; nothing to do", busid, server)
		return nil
	}
	out, err := s.run(ctx, "detach", "-p", port)
	if err != nil {
		return fmt.Errorf("usbip detach -p %s: %w (%s)", port, err, strings.TrimSpace(out))
	}
	return nil
}

// findLocalPort scans `usbip port` output for the entry matching
// `(server, busid)` and returns its decimal port number (e.g. `"00"`).
// Returns `("", nil)` when there is no match — this is the normal
// "already detached" case, not an error.
//
// The relevant `usbip port` output looks roughly like:
//
//	Imported USB devices
//	====================
//	Port 00: <Port in Use> at Full Speed(12Mbps)
//	       Yubico : YubiKey OTP+FIDO+CCID (1050:0407)
//	       4-1 -> usbip://192.168.5.2:3240/1-1
//	           -> remote bus/dev 001/002
//
// We track the current port number as we walk the lines and emit it
// as soon as we see a `usbip://HOST[:PORT]/BUSID` line that matches.
func (s *Shell) findLocalPort(ctx context.Context, server, busid string) (string, error) {
	out, err := s.run(ctx, "port")
	if err != nil {
		return "", fmt.Errorf("usbip port: %w (%s)", err, strings.TrimSpace(out))
	}
	return parseUsbipPort(out, server, busid), nil
}

// parseUsbipPort is the testable core of [Shell.findLocalPort]. Split
// out so the (somewhat fiddly) line-tracking logic can be exercised
// against fixtures without spawning `usbip`.
//
// Matching rules:
//
//   - The `usbip://` URL host is compared against `server` via
//     [hostsMatch], which defaults a missing port on either side to
//     [defaultUsbipPort]. Some `usbip-utils` builds elide the port
//     from the URL when it equals the default, so a literal compare
//     would miss those attachments and leak them on detach.
//   - The trailing path segment after the final `/` must equal `busid`.
func parseUsbipPort(out, server, busid string) string {
	server = strings.TrimSpace(server)
	for _, p := range parseUsbipPortAll(out) {
		if p.BusID == busid && hostsMatch(p.Server, server) {
			return p.Port
		}
	}
	return ""
}

// isAlreadyAttached detects the "already attached" error from `usbip`.
// The exact wording differs between usbip versions, so we match a
// small allow-list of known phrases rather than a single word like
// "already" (which would also swallow unrelated future errors that
// happen to contain it).
func isAlreadyAttached(out string) bool {
	lower := strings.ToLower(out)
	for _, needle := range []string{
		"already attached",
		"already used",
		"already in use",
		// "already bound" is generic kernel-driver vocabulary
		// ("driver X already bound to interface Y"); anchor it to
		// the device form so an unrelated driver-bind message in a
		// future usbip-utils build is not silently swallowed.
		"device already bound",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
