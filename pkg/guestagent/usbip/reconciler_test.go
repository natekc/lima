// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package usbip

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

// sampleListOutput mirrors what `usbip list -p -r HOST` emits. Two
// devices on one server, machine-parseable form.
const sampleListOutput = `1-1#usbid=1d6b:0002#
1-2#usbid=1050:0407#
`

func TestParseUsbipList(t *testing.T) {
	got := parseUsbipList(sampleListOutput)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d (%v)", len(got), got)
	}
	if got[1] != (RemoteDevice{BusID: "1-2", VendorID: "1050", ProductID: "0407"}) {
		t.Fatalf("entry[1]: %#v", got[1])
	}
	// Uppercase hex from a server is normalised to lowercase so it can
	// be compared against the validated config.
	upper := parseUsbipList("3-4#usbid=1D6B:000A#\n")
	if len(upper) != 1 || upper[0].VendorID != "1d6b" || upper[0].ProductID != "000a" {
		t.Fatalf("uppercase normalisation: %#v", upper)
	}
	// usbip-utils 2.0 ignores -p with -r and emits the human-readable
	// form unconditionally. Make sure we still extract busid+VID:PID.
	human := `usbip: info: using port 3240 ("3240")
Exportable USB devices
======================
 - host.lima.internal
       01-1: SanDisk Corp. : Cruzer (0781:5530)
           : /sys/devices/usb/01-1
           : (Defined at Interface level) (00/00/00)
           :  0 - Mass Storage / SCSI / Bulk-Only (08/06/50)
`
	humanGot := parseUsbipList(human)
	if len(humanGot) != 1 || humanGot[0] != (RemoteDevice{BusID: "01-1", VendorID: "0781", ProductID: "5530"}) {
		t.Fatalf("human-readable parse: %#v", humanGot)
	}
	// Some usbip-utils builds prefix the first field with "busid=";
	// strip it so the value matches a user-configured busid.
	prefixed := parseUsbipList("busid=1-3#usbid=1050:0407#\n")
	if len(prefixed) != 1 || prefixed[0] != (RemoteDevice{BusID: "1-3", VendorID: "1050", ProductID: "0407"}) {
		t.Fatalf("busid= prefix strip: %#v", prefixed)
	}
}

func TestParseUsbipPortAll(t *testing.T) {
	got := parseUsbipPortAll(samplePortOutput)
	if len(got) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(got))
	}
	if got[0] != (LocalAttachment{Port: "00", Server: "192.168.5.2:3240", BusID: "1-1"}) {
		t.Fatalf("entry[0]: %#v", got[0])
	}
}

// fakeShell satisfies both Attacher and Inspector by replaying canned
// data per server / global, and recording every Attach/Detach call so
// tests can assert convergence behaviour.
type fakeShell struct {
	exports   map[string][]RemoteDevice
	ports     []LocalAttachment
	listErr   error
	portErr   error
	attachErr error

	attached []string // "server|busid"
	detached []string
}

func (f *fakeShell) List(_ context.Context, server string) ([]RemoteDevice, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.exports[server], nil
}

func (f *fakeShell) Port(_ context.Context) ([]LocalAttachment, error) {
	if f.portErr != nil {
		return nil, f.portErr
	}
	return f.ports, nil
}

func (f *fakeShell) Attach(_ context.Context, server, busid string) error {
	f.attached = append(f.attached, server+"|"+busid)
	if f.attachErr != nil {
		// Caller wants a synthetic error (e.g. ErrAlreadyAttached) —
		// do NOT mutate ports; the kernel side effect didn't happen.
		return f.attachErr
	}
	// Simulate the kernel side effect so subsequent polls see it.
	f.ports = append(f.ports, LocalAttachment{Port: "x", Server: server, BusID: busid})
	return nil
}

func (f *fakeShell) Detach(_ context.Context, server, busid string) error {
	f.detached = append(f.detached, server+"|"+busid)
	filtered := f.ports[:0]
	for _, p := range f.ports {
		if p.Server == server && p.BusID == busid {
			continue
		}
		filtered = append(filtered, p)
	}
	f.ports = filtered
	return nil
}

func TestReconcile_AttachesBusidMatch(t *testing.T) {
	f := &fakeShell{
		exports: map[string][]RemoteDevice{
			"host:3240": {{BusID: "1-2", VendorID: "1050", ProductID: "0407"}},
		},
	}
	r := &Reconciler{Shell: f}
	devs := []Device{{BusID: "1-2", Server: "host:3240"}}

	if err := r.Reconcile(context.Background(), devs); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !equalSorted(f.attached, []string{"host:3240|1-2"}) {
		t.Fatalf("attached: %v", f.attached)
	}
	// Second pass must be idempotent — no new attaches, no detaches.
	if err := r.Reconcile(context.Background(), devs); err != nil {
		t.Fatalf("Reconcile #2: %v", err)
	}
	if len(f.attached) != 1 || len(f.detached) != 0 {
		t.Fatalf("second pass not idempotent: attached=%v detached=%v", f.attached, f.detached)
	}
}

func TestReconcile_AttachesVIDPIDMatch(t *testing.T) {
	f := &fakeShell{
		exports: map[string][]RemoteDevice{
			"host:3240": {
				{BusID: "1-1", VendorID: "1d6b", ProductID: "0002"}, // root hub, not configured
				{BusID: "1-2", VendorID: "1050", ProductID: "0407"}, // matches config
			},
		},
	}
	r := &Reconciler{Shell: f}
	devs := []Device{{VendorID: "1050", ProductID: "0407", Server: "host:3240"}}

	if err := r.Reconcile(context.Background(), devs); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !equalSorted(f.attached, []string{"host:3240|1-2"}) {
		t.Fatalf("expected only 1-2 attached, got %v", f.attached)
	}
}

func TestReconcile_DetachesWhenServerRemovedFromConfig(t *testing.T) {
	// Regression for the "removed-server cleanup" gap: when the user
	// drops the last device for a server from `usb.ip.devices`, the
	// next reconcile pass must still visit that server and detach
	// what we own. Before the fix, Reconcile only iterated servers
	// present in the desired list, so a config change that removed
	// the last device for a server left the attachment behind until
	// the VM stopped.
	f := &fakeShell{
		exports: map[string][]RemoteDevice{
			"host:3240": {{BusID: "1-2", VendorID: "1050", ProductID: "0407"}},
		},
	}
	r := &Reconciler{Shell: f}
	devs := []Device{{BusID: "1-2", Server: "host:3240"}}

	if err := r.Reconcile(context.Background(), devs); err != nil {
		t.Fatalf("Reconcile #1: %v", err)
	}
	if !equalSorted(f.attached, []string{"host:3240|1-2"}) {
		t.Fatalf("attached after #1: %v", f.attached)
	}
	// User removes the device (and therefore the server) entirely
	// from `usb.ip.devices`. Reconcile must still detach what we own.
	if err := r.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("Reconcile #2: %v", err)
	}
	if !equalSorted(f.detached, []string{"host:3240|1-2"}) {
		t.Fatalf("expected detach after removal, got %v", f.detached)
	}
	// And the per-server owned map should have been pruned.
	if _, leaked := r.owned["host:3240"]; leaked {
		t.Fatalf("r.owned still has entry for removed server")
	}
	// Third pass with the same empty config must be a no-op (no
	// extra detach calls).
	if err := r.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("Reconcile #3: %v", err)
	}
	if len(f.detached) != 1 {
		t.Fatalf("extra detach calls on second empty reconcile: %v", f.detached)
	}
}

func TestReconcile_DetachesWhenDeviceDisappears(t *testing.T) {
	f := &fakeShell{
		exports: map[string][]RemoteDevice{
			"host:3240": {{BusID: "1-2", VendorID: "1050", ProductID: "0407"}},
		},
	}
	r := &Reconciler{Shell: f}
	devs := []Device{{VendorID: "1050", ProductID: "0407", Server: "host:3240"}}

	// First pass: attach.
	if err := r.Reconcile(context.Background(), devs); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Device unplugged on host -> no longer exported.
	f.exports["host:3240"] = nil

	if err := r.Reconcile(context.Background(), devs); err != nil {
		t.Fatalf("Reconcile #2: %v", err)
	}
	if !equalSorted(f.detached, []string{"host:3240|1-2"}) {
		t.Fatalf("expected detach of 1-2, got %v", f.detached)
	}
}

func TestReconcile_DoesNotDetachForeignAttachments(t *testing.T) {
	f := &fakeShell{
		exports: map[string][]RemoteDevice{
			"host:3240": {},
		},
		// User attached this manually before lima started; we must
		// never detach it.
		ports: []LocalAttachment{{Port: "00", Server: "host:3240", BusID: "9-9"}},
	}
	r := &Reconciler{Shell: f}
	// Empty config: nothing to do.
	if err := r.Reconcile(context.Background(), []Device{{Server: "host:3240"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(f.detached) != 0 {
		t.Fatalf("foreign attachment was detached: %v", f.detached)
	}
}

func TestReconcile_PerServerErrorsAreIsolated(t *testing.T) {
	// Two servers; one is unreachable. The reachable one must still
	// converge.
	exports := map[string][]RemoteDevice{
		"good:3240": {{BusID: "1-2", VendorID: "1050", ProductID: "0407"}},
	}
	calls := 0
	f := &fakeListWrap{
		fakeShell: fakeShell{exports: exports},
		listFn: func(server string) ([]RemoteDevice, error) {
			calls++
			if server == "bad:3240" {
				return nil, errors.New("dial tcp: connection refused")
			}
			return exports[server], nil
		},
	}
	r := &Reconciler{Shell: f}
	devs := []Device{
		{BusID: "1-2", Server: "good:3240"},
		{BusID: "1-2", Server: "bad:3240"},
	}
	if err := r.Reconcile(context.Background(), devs); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !equalSorted(f.attached, []string{"good:3240|1-2"}) {
		t.Fatalf("attached: %v", f.attached)
	}
}

// TestReconcile_DetachesWhenRemovedServerIsUnreachable is a regression
// for v2.I1: when the user removes the last device for a server AND
// that server is now unreachable (the typical "I'm done with this
// host" workflow), the reconciler must still visit the server, skip
// the doomed remote List() call, and detach what we own locally.
func TestReconcile_DetachesWhenRemovedServerIsUnreachable(t *testing.T) {
	exports := map[string][]RemoteDevice{
		"host:3240": {{BusID: "1-2", VendorID: "1050", ProductID: "0407"}},
	}
	wrap := &fakeListWrap{
		fakeShell: fakeShell{exports: exports},
		listFn: func(server string) ([]RemoteDevice, error) {
			return exports[server], nil
		},
	}
	r := &Reconciler{Shell: wrap}
	if err := r.Reconcile(context.Background(), []Device{{BusID: "1-2", Server: "host:3240"}}); err != nil {
		t.Fatalf("Reconcile #1: %v", err)
	}
	if !equalSorted(wrap.attached, []string{"host:3240|1-2"}) {
		t.Fatalf("attached after #1: %v", wrap.attached)
	}
	// User removes the device from config AND the host is now
	// offline (List returns an error). Reconcile must still detach.
	wrap.listFn = func(server string) ([]RemoteDevice, error) {
		return nil, errors.New("dial tcp: connection refused")
	}
	if err := r.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("Reconcile #2: %v", err)
	}
	if !equalSorted(wrap.detached, []string{"host:3240|1-2"}) {
		t.Fatalf("expected detach despite List failure, got %v", wrap.detached)
	}
	if _, leaked := r.owned["host:3240"]; leaked {
		t.Fatalf("r.owned still has entry for removed/unreachable server")
	}
}

// TestReconcile_DoesNotAdoptPreExistingMatchingAttachment is a
// regression for v2.I2: when a user (or another tool) attached a
// device manually before the reconciler ran, and the user then
// added a config entry that matches that busid, the reconciler must
// NOT claim ownership of the pre-existing attachment. Later removing
// the config must therefore NOT detach a device we didn't attach.
func TestReconcile_DoesNotAdoptPreExistingMatchingAttachment(t *testing.T) {
	f := &fakeShell{
		exports: map[string][]RemoteDevice{
			"host:3240": {{BusID: "1-2", VendorID: "1050", ProductID: "0407"}},
		},
		// Pre-existing attachment — not made by this reconciler.
		ports: []LocalAttachment{{Port: "00", Server: "host:3240", BusID: "1-2"}},
	}
	r := &Reconciler{Shell: f}
	devs := []Device{{BusID: "1-2", Server: "host:3240"}}

	if err := r.Reconcile(context.Background(), devs); err != nil {
		t.Fatalf("Reconcile #1: %v", err)
	}
	if len(f.attached) != 0 {
		t.Fatalf("reconciler attached an already-attached device: %v", f.attached)
	}
	// Now user removes the config. Foreign attachment must persist.
	if err := r.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("Reconcile #2: %v", err)
	}
	if len(f.detached) != 0 {
		t.Fatalf("reconciler detached a foreign attachment: %v", f.detached)
	}
}

// TestReconcile_AttachReturningAlreadyAttachedDoesNotClaimOwnership
// covers the race where Port() reports no attachment but Attach()
// then returns ErrAlreadyAttached (some other process raced us).
// The reconciler must treat the result as foreign — no ownership
// record, no subsequent detach when config goes away.
func TestReconcile_AttachReturningAlreadyAttachedDoesNotClaimOwnership(t *testing.T) {
	f := &fakeShell{
		exports: map[string][]RemoteDevice{
			"host:3240": {{BusID: "1-2", VendorID: "1050", ProductID: "0407"}},
		},
		attachErr: ErrAlreadyAttached,
	}
	r := &Reconciler{Shell: f}
	devs := []Device{{BusID: "1-2", Server: "host:3240"}}

	if err := r.Reconcile(context.Background(), devs); err != nil {
		t.Fatalf("Reconcile #1: %v", err)
	}
	if _, claimed := r.owned["host:3240"]; claimed {
		t.Fatalf("reconciler claimed ownership of a racing attachment")
	}
	// Simulate the other process's attachment having materialised
	// in the kernel between ticks.
	f.ports = []LocalAttachment{{Port: "00", Server: "host:3240", BusID: "1-2"}}
	f.attachErr = nil
	if err := r.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("Reconcile #2: %v", err)
	}
	if len(f.detached) != 0 {
		t.Fatalf("reconciler detached an unowned attachment: %v", f.detached)
	}
}

// fakeListWrap lets a test override List() per call while keeping the
// rest of fakeShell behaviour.
type fakeListWrap struct {
	fakeShell
	listFn func(server string) ([]RemoteDevice, error)
}

func (f *fakeListWrap) List(_ context.Context, server string) ([]RemoteDevice, error) {
	return f.listFn(server)
}

func equalSorted(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	return strings.Join(g, ",") == strings.Join(w, ",")
}
