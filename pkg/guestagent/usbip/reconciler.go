// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package usbip

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// errLogInterval is the minimum gap between identical error log
// lines from the reconciler. A USB/IP server that's down would
// otherwise produce one line every poll interval, forever.
const errLogInterval = time.Minute

// Device is one configured USB/IP target. Exactly one of {BusID} or
// {VendorID + ProductID} is set; the reconciler treats the two modes
// uniformly.
type Device struct {
	BusID     string // e.g. "1-2"; empty when matching by VID/PID
	VendorID  string // 4-digit lowercase hex; empty when matching by busid
	ProductID string // 4-digit lowercase hex; empty when matching by busid
	Server    string // "host[:port]" as accepted by `usbip ... -r`
}

// RemoteDevice is one entry parsed from `usbip list -p -r SERVER`.
type RemoteDevice struct {
	BusID     string
	VendorID  string // lowercase hex
	ProductID string // lowercase hex
}

// LocalAttachment is one entry parsed from `usbip port`.
type LocalAttachment struct {
	Port   string // local vhci_hcd port number (e.g. "00")
	Server string // remote host[:port] as the device was attached with
	BusID  string // remote busid
}

// Inspector wraps the read-only `usbip` commands the reconciler needs.
// Split from [Attacher] so the [Reconciler] can declare a tight
// contract for fakes.
type Inspector interface {
	List(ctx context.Context, server string) ([]RemoteDevice, error)
	Port(ctx context.Context) ([]LocalAttachment, error)
}

// List runs `usbip [--tcp-port PORT] list -p -r HOST` and parses the
// machine-readable output. The `-p` flag prints `BUSID#usbid=VVVV:PPPP#`
// per line; we extract busid and (vid, pid) from each line. Lines we
// can't parse are skipped (forward-compat with future fields).
func (s *Shell) List(ctx context.Context, server string) ([]RemoteDevice, error) {
	if strings.TrimSpace(server) == "" {
		return nil, errors.New("usbip list: server must be non-empty")
	}
	host, port := splitServer(server)
	args := withTCPPort(port, "list", "-p", "-r", host)
	out, err := s.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("usbip list -p -r %s: %w (%s)", server, err, strings.TrimSpace(out))
	}
	return parseUsbipList(out), nil
}

// Port runs `usbip port` and returns every imported device as a
// (port, server, busid) triple.
func (s *Shell) Port(ctx context.Context) ([]LocalAttachment, error) {
	out, err := s.run(ctx, "port")
	if err != nil {
		return nil, fmt.Errorf("usbip port: %w (%s)", err, strings.TrimSpace(out))
	}
	return parseUsbipPortAll(out), nil
}

// humanListLine matches one device entry of the human-readable
// `usbip list -r HOST` output, e.g.
//
//	01-1: SanDisk Corp. : Cruzer (0781:5530)
//
// usbip-utils 2.0 silently ignores `-p/--parsable` for `-r`, so we
// have to scrape this form as a fallback.
var humanListLine = regexp.MustCompile(`^\s+([0-9]+-[0-9.]+):\s.*\(([0-9a-fA-F]{4}):([0-9a-fA-F]{4})\)\s*$`)

// parseUsbipList is the testable core of [Shell.List]. It accepts
// either the parsable per-record format
//
//	BUSID#usbid=VVVV:PPPP#
//
// emitted by `usbip list -p -r HOST` when honored, or the
// human-readable form some usbip-utils builds emit unconditionally.
// We only consume the two fields the reconciler matches against.
func parseUsbipList(out string) []RemoteDevice {
	var devs []RemoteDevice
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if busid, rest, ok := strings.Cut(trimmed, "#"); ok && busid != "" {
			// Some usbip-utils builds prefix the first field with
			// "busid=", e.g. `busid=1-1#usbid=VVVV:PPPP#`.
			if v, ok := strings.CutPrefix(busid, "busid="); ok {
				busid = v
			}
			var vid, pid string
			for _, field := range strings.Split(rest, "#") {
				if v, ok := strings.CutPrefix(field, "usbid="); ok {
					if a, b, ok := strings.Cut(v, ":"); ok {
						vid, pid = strings.ToLower(a), strings.ToLower(b)
					}
				}
			}
			if vid == "" || pid == "" {
				devs = append(devs, RemoteDevice{BusID: busid})
				continue
			}
			devs = append(devs, RemoteDevice{BusID: busid, VendorID: vid, ProductID: pid})
			continue
		}
		if m := humanListLine.FindStringSubmatch(line); m != nil {
			devs = append(devs, RemoteDevice{
				BusID:     m[1],
				VendorID:  strings.ToLower(m[2]),
				ProductID: strings.ToLower(m[3]),
			})
		}
	}
	return devs
}

// parseUsbipPortAll is the all-entries variant of [parseUsbipPort].
// Walks the same line-tracking state machine but emits every matching
// `(port, server, busid)` triple instead of stopping at the first
// match. Used by the reconciler to enumerate current attachments.
func parseUsbipPortAll(out string) []LocalAttachment {
	var (
		current string
		atts    []LocalAttachment
	)
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "Port "); ok {
			if i := strings.IndexByte(rest, ':'); i > 0 {
				current = strings.TrimSpace(rest[:i])
			}
			continue
		}
		if idx := strings.Index(trimmed, "usbip://"); idx >= 0 && current != "" {
			url := trimmed[idx+len("usbip://"):]
			slash := strings.LastIndexByte(url, '/')
			if slash < 0 {
				continue
			}
			atts = append(atts, LocalAttachment{
				Port:   current,
				Server: url[:slash],
				BusID:  url[slash+1:],
			})
		}
	}
	return atts
}

// Reconciler converges the kernel USB/IP stack toward a configured
// device list by polling each server and attaching/detaching as the
// remote inventory changes. It is the IP-native alternative to a
// push-based event RPC: simple, server-agnostic (works with any
// USB/IP daemon that speaks the kernel protocol), and self-healing
// across daemon restarts because every tick is a full reconcile.
//
// Exactly one Reconciler instance per guestagent. Call [Reconcile] on
// a ticker.
type Reconciler struct {
	// Shell is the combined Attacher + Inspector. Concrete production
	// callers pass `&Shell{}`; tests pass a fake.
	Shell interface {
		Attacher
		Inspector
	}

	// owned tracks (server, busid) tuples we attached this session.
	// Detach only fires for entries we own — never for foreign
	// attachments that the user may have made manually.
	owned map[string]map[string]struct{}

	// lastErrLog throttles repeated error messages. The key is an
	// opaque identifier ("list:SERVER", "port", "attach:SERVER:BUSID",
	// ...) and the value is the time of the last log. Cleared on
	// success for the same key.
	lastErrLog map[string]time.Time
}

// logErr emits err at warn level at most once per [errLogInterval]
// per key. The first occurrence always logs.
func (r *Reconciler) logErr(key string, err error, msg string, args ...any) {
	if r.lastErrLog == nil {
		r.lastErrLog = make(map[string]time.Time)
	}
	now := time.Now()
	if last, ok := r.lastErrLog[key]; ok && now.Sub(last) < errLogInterval {
		return
	}
	r.lastErrLog[key] = now
	logrus.WithError(err).Warnf(msg, args...)
}

// clearErr resets the throttle for key so the next failure for the
// same key logs immediately.
func (r *Reconciler) clearErr(key string) {
	delete(r.lastErrLog, key)
}

// Reconcile makes one polling pass over every distinct server in the
// device list. Errors against an individual server (daemon down,
// network blip, unknown busid) are logged and swallowed — the next
// tick will retry, so transient failures never propagate. Returns
// only context-cancellation errors.
func (r *Reconciler) Reconcile(ctx context.Context, devs []Device) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.owned == nil {
		r.owned = make(map[string]map[string]struct{})
	}

	byServer := groupByServer(devs)
	// Make sure servers we previously owned attachments on are
	// revisited even when no devices for them are configured now,
	// so removing the last device from `usb.ip.devices` (or removing
	// a whole server) still triggers detach on the next tick.
	for server := range r.owned {
		if _, ok := byServer[server]; !ok {
			byServer[server] = nil
		}
	}
	for _, server := range sortedKeys(byServer) {
		r.reconcileServer(ctx, server, byServer[server])
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// reconcileServer handles one server's worth of devices. Split out so
// per-server errors don't abort the whole pass.
func (r *Reconciler) reconcileServer(ctx context.Context, server string, devs []Device) {
	// Only query the remote when we have desired devices to resolve.
	// For a server that's been removed from `usb.ip.devices` (devs is
	// nil), we still need to visit it to detach what we own, but the
	// remote may be offline — calling List first would block cleanup
	// on the most plausible failure path (the user's "I'm done with
	// this host" workflow).
	var exports []RemoteDevice
	if len(devs) > 0 {
		listKey := "list:" + server
		var err error
		exports, err = r.Shell.List(ctx, server)
		if err != nil {
			r.logErr(listKey, err, "usbip reconcile: list %s failed", server)
			return
		}
		r.clearErr(listKey)
	}
	ports, err := r.Shell.Port(ctx)
	if err != nil {
		r.logErr("port", err, "usbip reconcile: port failed")
		return
	}
	r.clearErr("port")

	want, unresolved := wantedBusids(devs, exports)
	for _, id := range unresolved {
		// The server is reachable but does not currently export the
		// device the user asked for (typo, unplugged on the host,
		// not yet bound). Surface this so a misconfiguration isn't
		// silently invisible; throttle to one line per minute per id.
		r.logErr("unresolved:"+server+":"+id, nil,
			"usbip reconcile: configured device %s not exported by %s", id, server)
	}
	have := make(map[string]struct{})
	for _, p := range ports {
		if hostsMatch(p.Server, server) {
			have[p.BusID] = struct{}{}
		}
	}

	for _, busid := range sortedSetKeys(want) {
		if _, ok := have[busid]; ok {
			// Pre-existing attachment we did not make this session
			// (foreign attachment, or an attachment from a prior
			// agent run whose in-memory ownership was lost). Do NOT
			// claim ownership — adopting it would cause a later
			// config removal to detach a device we didn't attach,
			// violating the foreign-attachment guarantee.
			continue
		}
		attachKey := "attach:" + server + ":" + busid
		if err := r.Shell.Attach(ctx, server, busid); err != nil {
			if errors.Is(err, ErrAlreadyAttached) {
				// Race: attached between Port() and Attach(), by
				// someone else. Treat as foreign — don't own.
				r.clearErr(attachKey)
				continue
			}
			r.logErr(attachKey, err, "usbip reconcile: attach %s @ %s failed", busid, server)
			continue
		}
		r.clearErr(attachKey)
		r.ownDevice(server, busid)
		logrus.Infof("usbip reconcile: attached %s @ %s", busid, server)
	}
	for busid := range have {
		if _, wantIt := want[busid]; wantIt {
			continue
		}
		if _, mine := r.owned[server][busid]; !mine {
			continue
		}
		detachKey := "detach:" + server + ":" + busid
		if err := r.Shell.Detach(ctx, server, busid); err != nil {
			r.logErr(detachKey, err, "usbip reconcile: detach %s @ %s failed", busid, server)
			continue
		}
		r.clearErr(detachKey)
		r.disownDevice(server, busid)
		logrus.Infof("usbip reconcile: detached %s @ %s", busid, server)
	}
}

// ownDevice records (server, busid) as ours.
func (r *Reconciler) ownDevice(server, busid string) {
	if r.owned[server] == nil {
		r.owned[server] = make(map[string]struct{})
	}
	r.owned[server][busid] = struct{}{}
}

// disownDevice forgets (server, busid) and drops the per-server map
// once it's empty so a renamed/removed server doesn't leak forever.
// Also drops any throttle entries that key on this (server, busid):
// the next failure for an unrelated device on the same server should
// not be masked by a stale timestamp from a long-removed device.
func (r *Reconciler) disownDevice(server, busid string) {
	delete(r.owned[server], busid)
	if len(r.owned[server]) == 0 {
		delete(r.owned, server)
	}
	delete(r.lastErrLog, "attach:"+server+":"+busid)
	delete(r.lastErrLog, "detach:"+server+":"+busid)
}

// wantedBusids resolves each configured [Device] against the server's
// current export list and returns (want, unresolved). `want` is the
// set of busids the reconciler should ensure are attached; for
// busid-mode entries the configured busid is matched against export
// busids directly; VID:PID-mode entries match the first export with
// the same vid+pid (multi-device disambiguation by serial is a
// future addition). `unresolved` lists the human-readable identifier
// (busid, or "vid:pid") of each configured device that found no
// matching export — the caller is expected to log these (throttled)
// so the user can see that a configured device isn't currently
// present on the server.
func wantedBusids(devs []Device, exports []RemoteDevice) (want map[string]struct{}, unresolved []string) {
	want = make(map[string]struct{})
	for _, d := range devs {
		matched := false
		for _, e := range exports {
			switch {
			case d.BusID != "" && d.BusID == e.BusID:
				want[e.BusID] = struct{}{}
				matched = true
			case d.VendorID != "" && d.VendorID == e.VendorID && d.ProductID == e.ProductID:
				want[e.BusID] = struct{}{}
				matched = true
				// First match wins; without a serial we can't
				// distinguish multiple instances of the same model.
			default:
				continue
			}
			break
		}
		if !matched {
			id := d.BusID
			if id == "" {
				id = d.VendorID + ":" + d.ProductID
			}
			unresolved = append(unresolved, id)
		}
	}
	return want, unresolved
}

// groupByServer buckets devices by their Server field. Stable input
// order is preserved within each bucket.
func groupByServer(devs []Device) map[string][]Device {
	m := make(map[string][]Device)
	for _, d := range devs {
		m[d.Server] = append(m[d.Server], d)
	}
	return m
}

func sortedKeys(m map[string][]Device) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedSetKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
