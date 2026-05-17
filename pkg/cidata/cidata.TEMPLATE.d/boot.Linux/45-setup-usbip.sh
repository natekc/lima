#!/bin/sh

# SPDX-FileCopyrightText: Copyright The Lima Authors
# SPDX-License-Identifier: Apache-2.0

# Load the vhci_hcd kernel module so the lima-guestagent's USB/IP
# reconciler can attach remote devices. Doing this at boot (rather
# than from the agent) keeps module loading visible in cloud-init
# logs and out of the agent's runtime path.

set -eu

# Always attempt to load vhci_hcd, regardless of whether any USB/IP
# devices are configured at first boot. This lets a user add
# `usb.ip.devices` to a running VM via `limactl edit` + reload
# without having to reboot the guest. modprobe is cheap when the
# module is already loaded (or unavailable), so doing it
# unconditionally costs nothing.
#
# modprobe accepts both `vhci-hcd` and `vhci_hcd` (it aliases hyphens
# to underscores); the canonical upstream module name is `vhci_hcd`.
command -v modprobe >/dev/null 2>&1 || exit 0

# Silently attempt the load. The guestagent's attach path surfaces a
# clear error if a configured device cannot be attached due to a
# missing module, so a warning here would only noise up every guest
# whose kernel doesn't ship vhci_hcd — regardless of whether USB/IP
# is configured.
modprobe vhci_hcd >/dev/null 2>&1 || true
