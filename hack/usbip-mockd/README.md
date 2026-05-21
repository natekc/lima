# usbip-mockd

A ~200-line Go program that speaks just enough of the kernel
[USB/IP](https://docs.kernel.org/usb/usbip_protocol.html) wire protocol
(`OP_REQ_DEVLIST` and `OP_REQ_IMPORT`) to make `usbip attach` return
success. It advertises one synthetic device (Linux Foundation
Gadget Zero, `0525:a4a0`, busid `1-1`) and intentionally does not service
URBs after `IMPORT`.

## Purpose

Lets contributors verify the guest-side reconciler in
[`templates/experimental/usb-ip.yaml`](../../templates/experimental/usb-ip.yaml)
end-to-end **without owning the device, without root, and without
installing a real USB/IP daemon** (`usbipd-darwin`, `usbipd-mac`, Linux
`usbipd`).

## Usage

```sh
go run ./hack/usbip-mockd --listen 0.0.0.0:3240
```

Then start a guest whose `usb.ip.server` points at the host and whose
`usb.ip.devices` matches the advertised device. See the
[`Reproducing without a real USB device`](../../templates/experimental/usb-ip.yaml)
section of the template for a copy-paste recipe.

## What it does NOT simulate

After `OP_REQ_IMPORT` the mock keeps the TCP connection open but
never responds to URBs. The kernel `vhci_hcd` driver tears the
attachment down a few seconds later; the reconciler immediately
re-attaches on the next poll. This exercises the **attach control
path** but not actual USB data transfer.

`lsusb` inside the guest will therefore not show the synthetic
device; `journalctl -u lima-guestagent` is the authoritative signal.
To exercise data transfer too, you need a real USB/IP server with a
real device bound.
