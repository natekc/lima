// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package guestagent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/lima-vm/lima/v2/pkg/guestagent/usbip"
)

// usbipReconcileInterval controls how often the guestagent polls each
// USB/IP server for changes. 5s is a compromise between responsiveness
// to physical plug events and load on the server (one `usbip list -p`
// + one `usbip port` per server per tick).
const usbipReconcileInterval = 5 * time.Second

// limaEnvPath is the cidata-rendered KEY='VALUE' file the guestagent
// reads to discover USB/IP device config. Reading it directly (rather
// than relying on systemd's EnvironmentFile=) keeps the agent working
// on non-systemd init systems such as OpenRC.
const limaEnvPath = "/mnt/lima-cidata/lima.env"

// usbipEnvVar is the single cidata key that carries the marshaled
// device list. See pkg/cidata/template.go USBIPDevice for the schema.
const usbipEnvVar = "LIMA_CIDATA_USBIP_DEVICES_JSON"

// lastDecodeWarn remembers the raw env-var value that last produced a
// decode failure. The reconciler polls every [usbipReconcileInterval]
// (~5 s); without dedup, a malformed env file would flood the journal
// at ~12 lines/min indefinitely. Warning only when the failing value
// changes preserves visibility on the underlying problem (every fresh
// failure mode is logged once) without the flood.
var lastDecodeWarn struct {
	raw string
	set bool
}

// loadUSBIPDevices reads limaEnvPath and returns the configured
// devices. A missing file (no USB/IP configured, or the cidata mount
// is not present) is not an error.
func loadUSBIPDevices() []usbip.Device {
	f, err := os.Open(limaEnvPath)
	if err != nil {
		if !os.IsNotExist(err) {
			logrus.WithError(err).Debugf("usbip: open %s", limaEnvPath)
		}
		return nil
	}
	defer f.Close()
	raw, err := readUSBIPRaw(f)
	if err != nil {
		logrus.WithError(err).Warnf("usbip: scan %s", limaEnvPath)
		return nil
	}
	if raw == "" {
		lastDecodeWarn.set = false
		return nil
	}
	devs, err := decodeUSBIPDevices(raw)
	if err != nil {
		if !lastDecodeWarn.set || lastDecodeWarn.raw != raw {
			logrus.WithError(err).Warnf("usbip: decode %s", usbipEnvVar)
			lastDecodeWarn.raw = raw
			lastDecodeWarn.set = true
		}
		return nil
	}
	lastDecodeWarn.set = false
	return devs
}

// readUSBIPRaw scans lima.env and returns the value of usbipEnvVar
// (single-quote-unwrapped), or "" if not present. It is JSON-agnostic
// so the env-file parser stays trivial — the hard work is in
// decodeUSBIPDevices.
func readUSBIPRaw(r io.Reader) (string, error) {
	s := bufio.NewScanner(r)
	prefix := usbipEnvVar + "="
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		v := strings.TrimPrefix(line, prefix)
		if n := len(v); n >= 2 && v[0] == '\'' && v[n-1] == '\'' {
			v = v[1 : n-1]
		}
		return v, nil
	}
	return "", s.Err()
}

// decodeUSBIPDevices parses a JSON array of devices as written by
// pkg/cidata/cidata.go. Empty array or empty string yields nil.
func decodeUSBIPDevices(raw string) ([]usbip.Device, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var wire []struct {
		BusID     string `json:"busid,omitempty"`
		VendorID  string `json:"vendor_id,omitempty"`
		ProductID string `json:"product_id,omitempty"`
		Server    string `json:"server"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, err
	}
	if len(wire) == 0 {
		return nil, nil
	}
	devs := make([]usbip.Device, 0, len(wire))
	for _, w := range wire {
		if w.Server == "" {
			continue
		}
		devs = append(devs, usbip.Device{
			BusID:     w.BusID,
			VendorID:  w.VendorID,
			ProductID: w.ProductID,
			Server:    w.Server,
		})
	}
	return devs, nil
}

// startUSBIPReconciler spawns a background reconcile loop that
// re-reads the device list from `loader` on every tick. Re-reading
// each tick (rather than capturing devs at startup) is what lets the
// agent recover from the cidata-not-yet-mounted race on first boot:
// systemd starts lima-guestagent.service in parallel with
// cloud-init, so the very first read of /mnt/lima-cidata/lima.env
// usually returns nothing. The loop also runs forever even when the
// list is currently empty so that USB/IP devices can be added to a
// running VM via `limactl edit` + reload without restarting the
// agent.
//
// Kernel module loading (vhci-hcd) is intentionally NOT done here:
// it's handled by the boot.Linux/45-setup-usbip.sh cidata script so
// module state is established before the agent starts and is visible
// in cloud-init logs.
func startUSBIPReconciler(ctx context.Context, shell interface {
	usbip.Attacher
	usbip.Inspector
}, loader func() []usbip.Device,
) {
	r := &usbip.Reconciler{Shell: shell}
	// Startup is logged at Debug because the reconciler runs on every
	// Linux guest, including the overwhelming majority that have no
	// USB/IP configuration. The first non-empty load promotes to Info
	// so the watched-count is still visible when the feature is used.
	logrus.Debug("usbip reconciler: starting")
	go func() {
		t := time.NewTicker(usbipReconcileInterval)
		defer t.Stop()
		lastCount := -1
		for {
			devs := loader()
			if len(devs) != lastCount {
				// Skip the noisy initial "now watching 0" transition
				// (-1 -> 0) on guests that never configure USB/IP.
				if !(lastCount == -1 && len(devs) == 0) {
					logrus.Infof("usbip reconciler: now watching %d device(s)", len(devs))
				}
				lastCount = len(devs)
			}
			// Always call Reconcile, even when devs is empty: the
			// reconciler still needs to run to detach devices that
			// were just removed from `usb.ip.devices` via `limactl
			// edit` + reload.
			if err := r.Reconcile(ctx, devs); err != nil {
				return // context cancelled
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}
