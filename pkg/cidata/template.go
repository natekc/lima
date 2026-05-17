// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package cidata

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/lima-vm/lima/v2/pkg/identifiers"
	"github.com/lima-vm/lima/v2/pkg/iso9660util"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/textutil"
)

//go:embed cidata.TEMPLATE.d
var templateFS embed.FS

const templateFSRoot = "cidata.TEMPLATE.d"

type CACerts struct {
	RemoveDefaults *bool
	Trusted        []Cert
}

type Cert struct {
	Lines []string
}

type Containerd struct {
	System  bool
	User    bool
	Archive string
}
type Network struct {
	MACAddress string
	Interface  string
	Metric     uint32
}
type Mount struct {
	Tag        string
	MountPoint string // abs path, accessible by the User
	Type       string
	Options    string
}
type BootCmds struct {
	Lines []string
}

type DataFile struct {
	FileName    string
	Overwrite   string
	Owner       string
	Path        string
	Permissions string
}

type YQProvision struct {
	FileName    string
	Format      string
	Owner       string
	Path        string
	Permissions string
}

type Disk struct {
	Name   string
	Device string
	Format bool
	FSType string
	FSArgs []string
}

// USBIPDevice represents a single USB device to import via USB/IP,
// with the server address already resolved (defaults applied). At
// least one of {BusID} or {VendorID + ProductID} is set (validated
// upstream).
//
// The JSON tags are the on-wire schema: this struct is marshaled
// into the LIMA_CIDATA_USBIP_DEVICES_JSON env var that the guestagent
// reads back. Field renames are a breaking change for any guest
// running an older agent against a newer host.
type USBIPDevice struct {
	BusID     string `json:"busid,omitempty"`
	VendorID  string `json:"vendor_id,omitempty"`  // 4-digit lowercase hex; empty for busid-mode
	ProductID string `json:"product_id,omitempty"` // 4-digit lowercase hex; empty for busid-mode
	Server    string `json:"server"`               // host[:port], port defaulted upstream
}
type TemplateArgs struct {
	Debug                           bool
	OS                              limatype.OS
	Name                            string // instance name
	Hostname                        string // instance hostname
	IID                             string // instance id
	User                            string // user name
	Comment                         string // user information
	Home                            string // home directory
	Shell                           string // login shell
	UID                             uint32
	SSHPubKeys                      []string
	Mounts                          []Mount
	MountType                       string
	Disks                           []Disk
	GuestInstallPrefix              string
	UpgradePackages                 bool
	Containerd                      Containerd
	Networks                        []Network
	SlirpNICName                    string
	SlirpGateway                    string
	SlirpDNS                        string
	SlirpIPAddress                  string
	UDPDNSLocalPort                 int
	TCPDNSLocalPort                 int
	Env                             map[string]string
	Param                           map[string]string
	BootScripts                     bool
	DataFiles                       []DataFile
	YQProvisions                    []YQProvision
	DNSAddresses                    []string
	CACerts                         CACerts
	HostHomeMountPoint              string
	BootCmds                        []BootCmds
	RosettaEnabled                  bool
	RosettaBinFmt                   bool
	SkipDefaultDependencyResolution bool
	VMType                          string
	VSockPort                       int
	VirtioPort                      string
	Plain                           bool
	TimeZone                        string
	NoCloudInit                     bool
	USBIPDevicesJSON                string // marshaled []USBIPDevice; the on-wire form for the guestagent
}

func ValidateTemplateArgs(args *TemplateArgs) error {
	if err := identifiers.Validate(args.Name); err != nil {
		return err
	}
	// args.User is intentionally not validated here; the user can override with any name they want
	// limayaml.FillDefault will validate the default (local) username, but not an explicit setting
	if args.User == "root" {
		return errors.New("field User must not be \"root\"")
	}
	if args.UID == 0 {
		return errors.New("field UID must not be 0")
	}
	if args.Home == "" {
		return errors.New("field Home must be set")
	}
	if args.Shell == "" {
		return errors.New("field Shell must be set")
	}
	if len(args.SSHPubKeys) == 0 {
		return errors.New("field SSHPubKeys must be set")
	}
	for i, m := range args.Mounts {
		f := m.MountPoint
		if !path.IsAbs(f) {
			return fmt.Errorf("field mounts[%d] must be absolute, got %q", i, f)
		}
	}
	return nil
}

func ExecuteTemplateCloudConfig(args *TemplateArgs) ([]byte, error) {
	if err := ValidateTemplateArgs(args); err != nil {
		return nil, err
	}

	userData, err := templateFS.ReadFile(path.Join(templateFSRoot, "user-data"))
	if err != nil {
		return nil, err
	}

	cloudConfigYaml := string(userData)
	return textutil.ExecuteTemplate(cloudConfigYaml, args)
}

func ExecuteTemplateCIDataISO(args *TemplateArgs) ([]iso9660util.Entry, error) {
	if err := ValidateTemplateArgs(args); err != nil {
		return nil, err
	}

	fsys, err := fs.Sub(templateFS, templateFSRoot)
	if err != nil {
		return nil, err
	}

	var layout []iso9660util.Entry
	walkFn := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("got non-regular file %q", path)
		}
		templateB, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		b, err := textutil.ExecuteTemplate(string(templateB), args)
		if err != nil {
			return err
		}
		layout = append(layout, iso9660util.Entry{
			Path:   path,
			Reader: bytes.NewReader(b),
		})
		return nil
	}

	if err := fs.WalkDir(fsys, ".", walkFn); err != nil {
		return nil, err
	}

	return layout, nil
}
