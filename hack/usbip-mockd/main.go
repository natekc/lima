// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

// mockd is a minimal synthetic USB/IP server. It speaks just enough
// of the kernel USB/IP wire protocol to make `usbip list -p -r` and
// `usbip attach -r -b <busid>` succeed, advertising one fake device
// (Gadget Zero, 0525:a4a0, busid "1-1").
//
// Purpose: prove that the lima `usb.ip` reconciler interoperates
// with any server speaking the protocol, without depending on a
// real USB device, kernel modules, or a third-party daemon.
//
// What it implements:
//   - OP_REQ_DEVLIST  (0x8005) → OP_REP_DEVLIST  (0x0005) with 1 dev
//   - OP_REQ_IMPORT   (0x8003) → OP_REP_IMPORT   (0x0003), success
// After IMPORT the socket transitions to URB mode; we do not answer
// URBs. The kernel will eventually time out and tear the attachment
// down — but by then the `usbip attach` command (which is what the
// reconciler invokes) has already returned 0, which is what we are
// here to prove.
//
// Run on the host:
//   go run ./hack/usbip-mockd --listen 0.0.0.0:3240
// Then from inside a lima VM with this PR's `usb.ip` block pointing
// at host.lima.internal:3240, watch the reconciler attach the device.

package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

const (
	usbipVersion = 0x0111 // 1.1.1

	opReqDevlist = 0x8005
	opRepDevlist = 0x0005
	opReqImport  = 0x8003
	opRepImport  = 0x0003

	statusOK    = 0
	statusError = 1

	speedHigh = 3 // USB 2.0 high speed
)

// Synthetic exported device. Mirrors Linux Foundation's "Gadget Zero"
// because that's the device the docs/template example uses; the
// values are arbitrary apart from busid/vid/pid.
var device = exportedDevice{
	Path:    "/sys/devices/mock/usb1/1-1",
	BusID:   "1-1",
	BusNum:  1,
	DevNum:  2,
	Speed:   speedHigh,
	VID:     0x0525, // Linux Foundation
	PID:     0xa4a0, // Gadget Zero
	BCDDev:  0x0100,
	Class:   0xff, // vendor-specific
	NumCfgs: 1,
	NumIfcs: 1,
}

// exportedDevice is the wire-format 312-byte device record used by
// both OP_REP_DEVLIST and OP_REP_IMPORT.
type exportedDevice struct {
	Path    string // null-padded to 256
	BusID   string // null-padded to 32
	BusNum  uint32
	DevNum  uint32
	Speed   uint32
	VID     uint16
	PID     uint16
	BCDDev  uint16
	Class   uint8
	SubCls  uint8
	Proto   uint8
	CfgVal  uint8
	NumCfgs uint8
	NumIfcs uint8
}

func (d exportedDevice) marshal() []byte {
	b := make([]byte, 312)
	copy(b[0:256], d.Path)
	copy(b[256:288], d.BusID)
	binary.BigEndian.PutUint32(b[288:292], d.BusNum)
	binary.BigEndian.PutUint32(b[292:296], d.DevNum)
	binary.BigEndian.PutUint32(b[296:300], d.Speed)
	binary.BigEndian.PutUint16(b[300:302], d.VID)
	binary.BigEndian.PutUint16(b[302:304], d.PID)
	binary.BigEndian.PutUint16(b[304:306], d.BCDDev)
	b[306] = d.Class
	b[307] = d.SubCls
	b[308] = d.Proto
	b[309] = d.CfgVal
	b[310] = d.NumCfgs
	b[311] = d.NumIfcs
	return b
}

// 4-byte interface record per the USB/IP spec
// (`usbip_usb_interface`: class, subclass, protocol, padding).
// DEVLIST embeds one per interface after the device record; IMPORT
// does not include these.
func ifaceRecord() []byte {
	return []byte{0xff, 0x00, 0x00, 0x00}
}

func writeOpHeader(w io.Writer, cmd uint16, status uint32) error {
	var h [8]byte
	binary.BigEndian.PutUint16(h[0:2], usbipVersion)
	binary.BigEndian.PutUint16(h[2:4], cmd)
	binary.BigEndian.PutUint32(h[4:8], status)
	_, err := w.Write(h[:])
	return err
}

func handle(conn net.Conn) {
	defer conn.Close()
	peer := conn.RemoteAddr().String()

	var hdr [8]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		log.Printf("%s: read header: %v", peer, err)
		return
	}
	cmd := binary.BigEndian.Uint16(hdr[2:4])

	switch cmd {
	case opReqDevlist:
		log.Printf("%s: OP_REQ_DEVLIST", peer)
		if err := writeOpHeader(conn, opRepDevlist, statusOK); err != nil {
			return
		}
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], 1)
		if _, err := conn.Write(n[:]); err != nil {
			return
		}
		if _, err := conn.Write(device.marshal()); err != nil {
			return
		}
		if _, err := conn.Write(ifaceRecord()); err != nil {
			return
		}

	case opReqImport:
		// 32-byte busid follows the op header.
		var bbuf [32]byte
		if _, err := io.ReadFull(conn, bbuf[:]); err != nil {
			log.Printf("%s: read busid: %v", peer, err)
			return
		}
		busid := string(bytesUntilNUL(bbuf[:]))
		log.Printf("%s: OP_REQ_IMPORT busid=%q", peer, busid)

		if busid != device.BusID {
			_ = writeOpHeader(conn, opRepImport, statusError)
			return
		}
		if err := writeOpHeader(conn, opRepImport, statusOK); err != nil {
			return
		}
		if _, err := conn.Write(device.marshal()); err != nil {
			return
		}
		log.Printf("%s: IMPORT accepted; entering URB phase (no responses)", peer)
		// Hold the socket open until the kernel gives up.
		_, _ = io.Copy(io.Discard, conn)

	default:
		log.Printf("%s: unknown op 0x%04x; closing", peer, cmd)
		return
	}
}

func bytesUntilNUL(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

func main() {
	addr := flag.String("listen", "0.0.0.0:3240", "TCP listen address")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	log.Printf("mock usbip server: listening on %s (device busid=%s vid:pid=%04x:%04x)",
		*addr, device.BusID, device.VID, device.PID)

	// Close the listener on SIGINT/SIGTERM so `go run` / Ctrl-C
	// exits cleanly instead of requiring `kill -9`.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				os.Exit(0)
			}
			log.Printf("accept: %v", err)
			continue
		}
		go handle(c)
	}
}
