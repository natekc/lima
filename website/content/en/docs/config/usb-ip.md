---
title: USB/IP passthrough
weight: 95
---

Lima can import USB devices into a Linux guest from any host that
speaks the kernel [USB/IP](https://docs.kernel.org/usb/usbip_protocol.html)
wire protocol on TCP. The host runs any compatible USB/IP server; the
guest runs the kernel `vhci_hcd` driver as a client.

This is a network transport: Lima does not perform hypervisor-level USB
passthrough (native hypervisor-level USB passthrough on macOS is
tracked separately in [#4766](https://github.com/lima-vm/lima/issues/4766)).
The same configuration works under every Lima VM driver (vz, qemu,
krunkit, wsl2) without driver-specific implementation logic, because
the transport is TCP and the `host.lima.internal` alias resolves to
the host from inside every driver.

## Example

```yaml
usb:
  ip:
    server: host.lima.internal:3240   # optional; this is the default
    devices:
    # Static busid (always present at this address).
    - busid: 1-2
    # Auto-attach by VID:PID across unplug/replug. See "Hotplug" below.
    - vendorID: "1050"
      productID: "0407"
    - busid: 4-1
      server: other-host:3240         # per-device override
```

See [`templates/experimental/usb-ip.yaml`](https://github.com/lima-vm/lima/blob/master/templates/experimental/usb-ip.yaml)
for a full working example.

Exactly one of `{busid}` or `{vendorID, productID}` must be set per
device.

## Hotplug

A guest-side reconciler polls every configured USB/IP server every
few seconds. On each tick it lists the server's exported devices,
matches them against the configured `devices:` list (by busid or by
VID:PID), and converges the kernel's attachment state:

- Devices that match config but aren't attached yet -> `usbip attach`.
- Devices that we previously attached but the server no longer
  exports (e.g. unplugged on the host) -> `usbip detach`.
- Foreign attachments (anything the guest user attached manually) are
  never touched.

This is server-agnostic: any USB/IP daemon that speaks the kernel
protocol works. No host-side event socket or extra daemon is required.
The trade-off is latency: hotplug events are picked up within one poll
interval (~5 seconds) rather than instantly.

If a server is unreachable, the reconciler logs the error as a
warning and retries on the next tick; other servers continue to
work. Identical messages are throttled to one log line per minute
so a permanently misconfigured server does not flood the journal.

VID:PID matching is *first-match-wins* against the server's export
list: when the host exports multiple instances of the same
`vendorID`/`productID` (e.g. two identical USB keys), exactly one
is bound and which one is implementation-defined. Use `busid:` for
deterministic addressing when you have duplicate devices.

## Prerequisites

### Host

1. A USB/IP server is listening (default `host.lima.internal:3240`).
2. The devices you want to share are bound / captured by that server.
   Each implementation has its own command, e.g. `usbipd bind --busid 1-2`
   on Linux.

### Guest

The `usbip` userspace tool must be present. Package names vary; on
Debian/Ubuntu it is `usbip`. Install it via `provision:` rather than
relying on Lima to do it at boot -- boot-time package installs are
slow and fragile on air-gapped networks. See the example template
above.

The kernel `vhci_hcd` module is loaded by a cidata boot script on
every boot, regardless of whether `usb.ip.devices` is set, so that
the module is already present if you later add a device via
`limactl edit`. `limactl edit` requires a stopped instance, so the
flow is `limactl stop <name> && limactl edit <name> && limactl
start <name>`; on next boot the new device list is picked up
automatically. Module loading happens before the agent starts and
is visible in cloud-init logs.

## Limitations

- Linux guests only. The `vhci_hcd` driver is Linux-specific.
- IPv4 / DNS hostnames only; IPv6 server addresses are rejected at
  validation because most `usbip(8)` builds cannot parse a bare IPv6
  in the `-r` argument and the path has not been exercised end to end.
- No automatic detach on `limactl stop`. Clients release their slot on
  TCP close, which a graceful stop triggers, but for unclean shutdowns
  the server-side daemon is expected to notice the dropped connection.
- Add-latency is bounded by the poll interval (~5 s). Plugging a
  device on the host that matches a configured VID:PID can take up
  to one tick to attach.
- VID:PID matches the first qualifying export; identical duplicates
  are not disambiguated. Use `busid:` for deterministic addressing.
- Not exercised in CI. Each (driver, server) combination should work
  in principle because the transport is plain TCP, but only manual
  testing has been done so far.
- Attachment ownership is in-memory in the guestagent. If the agent
  restarts (e.g. crash, manual `systemctl restart lima-guestagent`)
  while a Lima-attached device is still imported, the surviving
  attachment becomes indistinguishable from a foreign attachment on
  the next reconcile, and later removing the device from
  `usb.ip.devices` will no longer detach it. Workaround: detach
  manually with `sudo usbip detach -p <port>` inside the guest before
  removing the entry, or stop and restart the VM.
