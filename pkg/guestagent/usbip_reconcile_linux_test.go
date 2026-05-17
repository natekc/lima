// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package guestagent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lima-vm/lima/v2/pkg/guestagent/usbip"
)

// fromEnvLines simulates a cidata lima.env built from KEY=VALUE lines
// and runs the same code path the production agent uses.
func fromEnvLines(lines ...string) []usbip.Device {
	raw, err := readUSBIPRaw(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil {
		panic(err)
	}
	devs, err := decodeUSBIPDevices(raw)
	if err != nil {
		panic(err)
	}
	return devs
}

func TestDecodeUSBIPDevices(t *testing.T) {
	t.Run("empty string yields nil", func(t *testing.T) {
		got, err := decodeUSBIPDevices("")
		if err != nil || got != nil {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("empty array yields nil", func(t *testing.T) {
		got, err := decodeUSBIPDevices("[]")
		if err != nil || got != nil {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("busid mode", func(t *testing.T) {
		got := fromEnvLines(
			`LIMA_CIDATA_USBIP_DEVICES_JSON='[{"busid":"1-2","server":"host.lima.internal:3240"}]'`,
		)
		want := []usbip.Device{{BusID: "1-2", Server: "host.lima.internal:3240"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v want %#v", got, want)
		}
	})
	t.Run("vid pid mode preserves all fields", func(t *testing.T) {
		got := fromEnvLines(
			`LIMA_CIDATA_USBIP_DEVICES_JSON='[{"vendor_id":"1050","product_id":"0407","server":"other:9999"}]'`,
		)
		want := []usbip.Device{{VendorID: "1050", ProductID: "0407", Server: "other:9999"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v want %#v", got, want)
		}
	})
	t.Run("entry without server is dropped", func(t *testing.T) {
		// Defensive: the host-side code never emits server=""; this
		// just guards against a partially-rendered file.
		got, err := decodeUSBIPDevices(`[{"busid":"1-2"}]`)
		if err != nil || got != nil {
			t.Fatalf("expected nil/nil, got %v / %v", got, err)
		}
	})
	t.Run("unrelated env vars ignored", func(t *testing.T) {
		got := fromEnvLines(
			"PATH=/usr/bin",
			"LIMA_CIDATA_NAME=foo",
			`LIMA_CIDATA_USBIP_DEVICES_JSON='[{"busid":"1-2","server":"h:3240"}]'`,
		)
		if len(got) != 1 || got[0].BusID != "1-2" {
			t.Fatalf("unexpected: %#v", got)
		}
	})
	t.Run("malformed JSON returns error", func(t *testing.T) {
		_, err := decodeUSBIPDevices(`[`)
		if err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestReadUSBIPRaw(t *testing.T) {
	t.Run("absent returns empty", func(t *testing.T) {
		got, err := readUSBIPRaw(strings.NewReader("FOO=bar\nBAZ='quux'\n"))
		if err != nil || got != "" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("unwraps single quotes", func(t *testing.T) {
		got, err := readUSBIPRaw(strings.NewReader(
			`LIMA_CIDATA_USBIP_DEVICES_JSON='[{"busid":"1-2","server":"h:3240"}]'` + "\n",
		))
		if err != nil {
			t.Fatal(err)
		}
		if got != `[{"busid":"1-2","server":"h:3240"}]` {
			t.Fatalf("unexpected: %q", got)
		}
	})
}
