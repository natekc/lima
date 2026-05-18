// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package limayaml

import (
	"fmt"
	"os"
	"testing"

	"github.com/goccy/go-yaml"
	"gotest.tools/v3/assert"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/ptr"
	"github.com/lima-vm/lima/v2/pkg/version"
)

func TestValidateEmpty(t *testing.T) {
	y, err := Load(t.Context(), []byte{}, "empty.yaml")
	assert.NilError(t, err)
	err = Validate(y, false)
	assert.Error(t, err, "field `images` must be set")
}

func TestValidateMinimumLimaVersion(t *testing.T) {
	images := `images: [{"location": "/"}]`

	tests := []struct {
		name               string
		currentVersion     string
		minimumLimaVersion string
		wantErr            string
	}{
		{
			name:               "minimumLimaVersion less than current version",
			currentVersion:     "1.1.1-114-g5bf5e513",
			minimumLimaVersion: "1.1.0",
			wantErr:            "",
		},
		{
			name:               "minimumLimaVersion greater than current version",
			currentVersion:     "1.1.1-114-g5bf5e513",
			minimumLimaVersion: "1.1.2",
			wantErr:            `template requires Lima version "1.1.2"; this is only "1.1.1-114-g5bf5e513"`,
		},
		{
			name:               "invalid current version",
			currentVersion:     "<unknown>",
			minimumLimaVersion: "0.8.0",
			wantErr:            "", // Unparsable versions are treated as "latest"
		},
		{
			name:               "invalid minimumLimaVersion",
			currentVersion:     "1.1.1-114-g5bf5e513",
			minimumLimaVersion: "invalid",
			wantErr:            "field `minimumLimaVersion` must be a semvar value, got \"invalid\": invalid is not in dotted-tri format", // Only parse error, no comparison error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldVersion := version.Version
			version.Version = tt.currentVersion
			t.Cleanup(func() { version.Version = oldVersion })

			y, err := Load(t.Context(), []byte("minimumLimaVersion: "+tt.minimumLimaVersion+"\n"+images), "lima.yaml")
			assert.NilError(t, err)

			err = Validate(y, false)
			if tt.wantErr == "" {
				assert.NilError(t, err)
			} else {
				assert.Error(t, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDigest(t *testing.T) {
	images := `images: [{"location": "https://cloud-images.ubuntu.com/releases/oracular/release-20250701/ubuntu-24.10-server-cloudimg-amd64.img",digest: "69f31d3208895e5f646e345fbc95190e5e311ecd1359a4d6ee2c0b6483ceca03"}]`
	validProbe := `probes: [{"script": "#!foo"}]`
	y, err := Load(t.Context(), []byte(validProbe+"\n"+images), "lima.yaml")
	assert.NilError(t, err)
	err = Validate(y, false)
	assert.Error(t, err, "field `images[0].digest` is invalid: 69f31d3208895e5f646e345fbc95190e5e311ecd1359a4d6ee2c0b6483ceca03: invalid checksum digest format")

	images2 := `images: [{"location": "https://cloud-images.ubuntu.com/releases/oracular/release-20250701/ubuntu-24.10-server-cloudimg-amd64.img",digest: "sha001:69f31d3208895e5f646e345fbc95190e5e311ecd1359a4d6ee2c0b6483ceca03"}]`
	y2, err := Load(t.Context(), []byte(validProbe+"\n"+images2), "lima.yaml")
	assert.NilError(t, err)
	err = Validate(y2, false)
	assert.Error(t, err, "field `images[0].digest` is invalid: sha001:69f31d3208895e5f646e345fbc95190e5e311ecd1359a4d6ee2c0b6483ceca03: unsupported digest algorithm")
}

func TestValidateProbes(t *testing.T) {
	images := `images: [{"location": "/"}]`
	validProbe := `probes: [{"script": "#!foo"}]`
	y, err := Load(t.Context(), []byte(validProbe+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.NilError(t, err)

	invalidProbe := `probes: [{"script": "foo"}]`
	y, err = Load(t.Context(), []byte(invalidProbe+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.Error(t, err, "field `probe[0].script` must start with a '#!' line")

	invalidProbe = `probes: [{file: {digest: decafbad}}]`
	y, err = Load(t.Context(), []byte(invalidProbe+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.Error(t, err, "field `probe[0].file.digest` support is not yet implemented\n"+
		"field `probe[0].script` must start with a '#!' line")
}

func TestValidateProvisionMode(t *testing.T) {
	images := `images: [{location: /}]`
	provisionBoot := `provision: [{mode: boot, script: "touch /tmp/param-$PARAM_BOOT"}]`
	y, err := Load(t.Context(), []byte(provisionBoot+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.NilError(t, err)

	provisionUser := `provision: [{mode: user, script: "touch /tmp/param-$PARAM_USER"}]`
	y, err = Load(t.Context(), []byte(provisionUser+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.NilError(t, err)

	provisionDependency := `provision: [{mode: ansible, script: "touch /tmp/param-$PARAM_DEPENDENCY"}]`
	y, err = Load(t.Context(), []byte(provisionDependency+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.NilError(t, err)

	provisionInvalid := `provision: [{mode: invalid}]`
	y, err = Load(t.Context(), []byte(provisionInvalid+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.Error(t, err, "field `provision[0].mode` must one of \"system\", \"user\", \"boot\", \"data\", \"dependency\", \"ansible\", or \"yq\"\n"+
		"field `provision[0].script` must not be empty")
}

func TestValidateProvisionData(t *testing.T) {
	images := `images: [{location: /}]`
	validData := `provision: [{mode: data, path: /tmp, content: hello}]`
	y, err := Load(t.Context(), []byte(validData+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.NilError(t, err)

	invalidData := `provision: [{mode: data, content: hello}]`
	y, err = Load(t.Context(), []byte(invalidData+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.Error(t, err, "field `provision[0].path` must not be empty when mode is \"data\"")

	invalidData = `provision: [{mode: data, path: /tmp, content: hello, permissions: 9}]`
	y, err = Load(t.Context(), []byte(invalidData+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.ErrorContains(t, err, "provision[0].permissions` must be an octal number")
}

func TestValidateProvisionYQ(t *testing.T) {
	images := `images: [{location: /}]`
	param := `param: {"cdi": "true"}`
	// Valid
	validYQProvision := `provision: [{mode: yq, expression: ".features.cdi={{.Param.cdi}}", path: /tmp}]`
	y, err := Load(t.Context(), []byte(param+"\n"+validYQProvision+"\n"+images), "lima.yaml")
	assert.NilError(t, err)
	err = Validate(y, false)
	assert.NilError(t, err)

	// Missing path
	invalidYQProvision := `provision: [{mode: yq, expression: ".features.cdi={{.Param.cdi}}"}]`
	y, err = Load(t.Context(), []byte(param+"\n"+invalidYQProvision+"\n"+images), "lima.yaml")
	assert.NilError(t, err)
	err = Validate(y, false)
	assert.ErrorContains(t, err, "field `provision[0].path` must not be empty when mode is \"yq\"")

	// non-absolute path
	invalidYQProvision = `provision: [{mode: yq, expression: ".features.cdi={{.Param.cdi}}", path: tmp}]`
	y, err = Load(t.Context(), []byte(param+"\n"+invalidYQProvision+"\n"+images), "lima.yaml")
	assert.NilError(t, err)
	err = Validate(y, false)
	assert.ErrorContains(t, err, "field `provision[0].path` must be an absolute path")

	// Missing expression
	invalidYQProvision = `provision: [{mode: yq, path: "/{{.Param.cdi}}"}]`
	y, err = Load(t.Context(), []byte(param+"\n"+invalidYQProvision+"\n"+images), "lima.yaml")
	assert.NilError(t, err)
	err = Validate(y, false)
	assert.ErrorContains(t, err, "field `provision[0].expression` must not be empty when mode is \"yq\"")

	// Invalid permissions
	invalidYQProvision = `provision: [{mode: yq, expression: ".features.cdi={{.Param.cdi}}", path: /tmp, permissions: 9}]`
	y, err = Load(t.Context(), []byte(param+"\n"+invalidYQProvision+"\n"+images), "lima.yaml")
	assert.NilError(t, err)
	err = Validate(y, false)
	assert.ErrorContains(t, err, "provision[0].permissions` must be an octal number")
}

func TestValidateAdditionalDisks(t *testing.T) {
	images := `images: [{"location": "/"}]`

	validDisks := `
additionalDisks:
  - name: "disk1"
  - name: "disk2"
`
	y, err := Load(t.Context(), []byte(validDisks+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.NilError(t, err)

	invalidDisks := `
additionalDisks:
  - name: ""
`
	y, err = Load(t.Context(), []byte(invalidDisks+"\n"+images), "lima.yaml")
	assert.NilError(t, err)

	err = Validate(y, false)
	assert.Error(t, err, "field `additionalDisks[0].name is invalid`: identifier must not be empty")
}

func TestValidateParamName(t *testing.T) {
	images := `images: [{"location": "/"}]`
	validProvision := `provision: [{"script": "echo $PARAM_name $PARAM_NAME $PARAM_Name_123"}]`
	validParam := []string{
		`param: {"name": "value"}`,
		`param: {"NAME": "value"}`,
		`param: {"Name_123": "value"}`,
	}
	for _, param := range validParam {
		y, err := Load(t.Context(), []byte(param+"\n"+validProvision+"\n"+images), "lima.yaml")
		assert.NilError(t, err)

		err = Validate(y, false)
		assert.NilError(t, err)
	}

	invalidProvision := `provision: [{"script": "echo $PARAM__Name $PARAM_3Name $PARAM_Last.Name"}]`
	invalidParam := []string{
		`param: {"_Name": "value"}`,
		`param: {"3Name": "value"}`,
		`param: {"Last.Name": "value"}`,
	}
	for _, param := range invalidParam {
		y, err := Load(t.Context(), []byte(param+"\n"+invalidProvision+"\n"+images), "lima.yaml")
		assert.NilError(t, err)

		err = Validate(y, false)
		assert.ErrorContains(t, err, "name does not match regex")
	}
}

func TestValidateParamValue(t *testing.T) {
	images := `images: [{"location": "/"}]`
	provision := `provision: [{"script": "echo $PARAM_name"}]`
	validParam := []string{
		`param: {"name": ""}`,
		`param: {"name": "foo bar"}`,
		`param: {"name": "foo\tbar"}`,
		`param: {"name": "Symbols ½ and emoji → 👀"}`,
	}
	for _, param := range validParam {
		y, err := Load(t.Context(), []byte(param+"\n"+provision+"\n"+images), "lima.yaml")
		assert.NilError(t, err)

		err = Validate(y, false)
		assert.NilError(t, err)
	}

	invalidParam := []string{
		`param: {"name": "The end.\n"}`,
		`param: {"name": "\r"}`,
	}
	for _, param := range invalidParam {
		y, err := Load(t.Context(), []byte(param+"\n"+provision+"\n"+images), "lima.yaml")
		assert.NilError(t, err)

		err = Validate(y, false)
		assert.ErrorContains(t, err, "value contains unprintable character")
	}
}

func TestValidateParamIsUsed(t *testing.T) {
	paramYaml := `param:
  name: value`
	_, err := Load(t.Context(), []byte(paramYaml), "paramIsNotUsed.yaml")
	assert.Error(t, err, "field `param` key \"name\" is not used in any provision, probe, copyToHost, or portForward")

	fieldsUsingParam := []string{
		`mounts: [{"location": "/tmp/{{ .Param.name }}"}]`,
		`mounts: [{"location": "/tmp", mountPoint: "/tmp/{{ .Param.name }}"}]`,
		`provision: [{"script": "echo {{ .Param.name }}"}]`,
		`provision: [{"script": "echo $PARAM_name"}]`,
		`probes: [{"script": "echo {{ .Param.name }}"}]`,
		`probes: [{"script": "echo $PARAM_name"}]`,
		`copyToHost: [{"guest": "/tmp/{{ .Param.name }}", "host": "/tmp"}]`,
		`copyToHost: [{"guest": "/tmp", "host": "/tmp/{{ .Param.name }}"}]`,
		`portForwards: [{"guestSocket": "/tmp/{{ .Param.name }}", "hostSocket": "/tmp"}]`,
		`portForwards: [{"guestSocket": "/tmp", "hostSocket": "/tmp/{{ .Param.name }}"}]`,
	}
	for _, fieldUsingParam := range fieldsUsingParam {
		_, err = Load(t.Context(), []byte(fieldUsingParam+"\n"+paramYaml), "paramIsUsed.yaml")
		//
		assert.NilError(t, err)
	}

	// use "{{if eq .Param.rootful \"true\"}}…{{else}}…{{end}}" in provision, probe, copyToHost, and portForward
	rootfulYaml := `param:
  rootful: true`
	fieldsUsingIfParamRootfulTrue := []string{
		`mounts: [{"location": "/tmp/{{if eq .Param.rootful \"true\"}}rootful{{else}}rootless{{end}}", "mountPoint": "/tmp"}]`,
		`mounts: [{"location": "/tmp", "mountPoint": "/tmp/{{if eq .Param.rootful \"true\"}}rootful{{else}}rootless{{end}}"}]`,
		`provision: [{"script": "echo {{if eq .Param.rootful \"true\"}}rootful{{else}}rootless{{end}}"}]`,
		`probes: [{"script": "echo {{if eq .Param.rootful \"true\"}}rootful{{else}}rootless{{end}}"}]`,
		`copyToHost: [{"guest": "/tmp/{{if eq .Param.rootful \"true\"}}rootful{{else}}rootless{{end}}", "host": "/tmp"}]`,
		`copyToHost: [{"guest": "/tmp", "host": "/tmp/{{if eq .Param.rootful \"true\"}}rootful{{else}}rootless{{end}}"}]`,
		`portForwards: [{"guestSocket": "{{if eq .Param.rootful \"true\"}}/var/run{{else}}/run/user/{{.UID}}{{end}}/docker.sock", "hostSocket": "{{.Dir}}/sock/docker.sock"}]`,
		`portForwards: [{"guestSocket": "/var/run/docker.sock", "hostSocket": "{{.Dir}}/sock/docker-{{if eq .Param.rootful \"true\"}}rootful{{else}}rootless{{end}}.sock"}]`,
	}
	for _, fieldUsingIfParamRootfulTrue := range fieldsUsingIfParamRootfulTrue {
		_, err = Load(t.Context(), []byte(fieldUsingIfParamRootfulTrue+"\n"+rootfulYaml), "paramIsUsed.yaml")
		//
		assert.NilError(t, err)
	}

	// use rootFul instead of rootful
	rootFulYaml := `param:
  rootFul: true`
	for _, fieldUsingIfParamRootfulTrue := range fieldsUsingIfParamRootfulTrue {
		_, err = Load(t.Context(), []byte(fieldUsingIfParamRootfulTrue+"\n"+rootFulYaml), "paramIsUsed.yaml")
		//
		assert.Error(t, err, "field `param` key \"rootFul\" is not used in any provision, probe, copyToHost, or portForward")
	}
}

func TestValidateMultipleErrors(t *testing.T) {
	yamlWithMultipleErrors := `
os: windows
arch: unsupported_arch
portForwards:
  - guestPort: 22
    hostPort: 2222
  - guestPort: 8080
    hostPort: 65536
provision:
  - mode: invalid_mode
    script: echo test
  - mode: data
    content: test
`

	y, err := Load(t.Context(), []byte(yamlWithMultipleErrors), "multiple-errors.yaml")
	assert.NilError(t, err)
	err = Validate(y, false)
	t.Logf("Validation errors: %v", err)

	assert.Error(t, err, "field `os` must be one of [\"Linux\" \"Darwin\" \"FreeBSD\"]; got \"windows\"\n"+
		"field `arch` must be one of [x86_64 aarch64 armv7l ppc64le riscv64 s390x]; got \"unsupported_arch\"\n"+
		"field `images` must be set\n"+
		"field `provision[0].mode` must one of \"system\", \"user\", \"boot\", \"data\", \"dependency\", \"ansible\", or \"yq\"\n"+
		"field `provision[1].path` must not be empty when mode is \"data\"")
}

func TestValidateAgainstLatestConfig(t *testing.T) {
	tests := []struct {
		name    string
		yNew    string
		yLatest string
		wantErr string
	}{
		{
			name:    "Valid disk size unchanged",
			yNew:    `disk: 100GiB`,
			yLatest: `disk: 100GiB`,
			wantErr: fmt.Sprintf("failed to resolve vm for \"\": vmType %q is not a registered driver", limatype.DefaultDriver()),
		},
		{
			name:    "Valid disk size increased",
			yNew:    `disk: 200GiB`,
			yLatest: `disk: 100GiB`,
			wantErr: fmt.Sprintf("failed to resolve vm for \"\": vmType %q is not a registered driver", limatype.DefaultDriver()),
		},
		{
			name:    "No disk field in both YAMLs",
			yNew:    ``,
			yLatest: ``,
			wantErr: fmt.Sprintf("failed to resolve vm for \"\": vmType %q is not a registered driver", limatype.DefaultDriver()),
		},
		{
			name:    "No disk field in new YAMLs",
			yNew:    ``,
			yLatest: `disk: 100GiB`,
			wantErr: fmt.Sprintf("failed to resolve vm for \"\": vmType %q is not a registered driver", limatype.DefaultDriver()),
		},
		{
			name:    "No disk field in latest YAMLs",
			yNew:    `disk: 100GiB`,
			yLatest: ``,
			wantErr: fmt.Sprintf("failed to resolve vm for \"\": vmType %q is not a registered driver", limatype.DefaultDriver()),
		},
		{
			name:    "Disk size shrunk",
			yNew:    `disk: 50GiB`,
			yLatest: `disk: 100GiB`,
			wantErr: fmt.Sprintf("failed to resolve vm for \"\": vmType %q is not a registered driver\n", limatype.DefaultDriver()) +
				"field `disk`: shrinking the disk (100GiB --> 50GiB) is not supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgainstLatestConfig(t.Context(), []byte(tt.yNew), []byte(tt.yLatest))
			assert.Error(t, err, tt.wantErr)
		})
	}
}

func TestValidateUSBIPServer(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"", true},
		{" ", true},
		{"example.com", false},
		{"example.com:3240", false},
		{"127.0.0.1:3240", false},
		{"host:", true},                // empty port
		{":3240", true},                // empty host
		{"host:abcd", true},            // non-numeric port
		{"host:0", true},               // port out of range (low)
		{"host:65536", true},           // port out of range (high)
		{"evil;rm:3240", true},         // shell metachar in host
		{"host with space:3240", true}, // whitespace in host
		{"$(touch /tmp/x)", true},      // command substitution
		{"-bad.example:3240", true},    // leading hyphen
		{"bad-.example:3240", true},    // trailing hyphen in label boundary
		{"[::1]", true},                // IPv6 not supported
		{"[::1]:3240", true},           // IPv6 not supported
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			err := validateUSBIPServer(tc.in)
			if tc.wantErr {
				assert.Assert(t, err != nil, "expected error for %q", tc.in)
			} else {
				assert.NilError(t, err)
			}
		})
	}
}

func TestValidateUSBIPDevices(t *testing.T) {
	images := `images: [{"location": "/"}]`
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "ok minimal busid",
			yaml:    images + "\nusb: {ip: {devices: [{busid: '1-2'}]}}\n",
			wantErr: "",
		},
		{
			name:    "ok dotted busid",
			yaml:    images + "\nusb: {ip: {devices: [{busid: '01-1.4'}]}}\n",
			wantErr: "",
		},
		{
			name:    "ok vid pid",
			yaml:    images + "\nusb: {ip: {devices: [{vendorID: '1050', productID: '0407'}]}}\n",
			wantErr: "",
		},
		{
			name:    "neither busid nor vid",
			yaml:    images + "\nusb: {ip: {devices: [{}]}}\n",
			wantErr: "must set either `busid` or both `vendorID` and `productID`",
		},
		{
			name:    "busid and vid mutually exclusive",
			yaml:    images + "\nusb: {ip: {devices: [{busid: '1-2', vendorID: '1050', productID: '0407'}]}}\n",
			wantErr: "mutually exclusive",
		},
		{
			name:    "vid without pid",
			yaml:    images + "\nusb: {ip: {devices: [{vendorID: '1050'}]}}\n",
			wantErr: "must be set together",
		},
		{
			name:    "pid without vid",
			yaml:    images + "\nusb: {ip: {devices: [{productID: '0407'}]}}\n",
			wantErr: "must be set together",
		},
		{
			name:    "vid uppercase is canonicalised to lowercase",
			yaml:    images + "\nusb: {ip: {devices: [{vendorID: '1050', productID: '04FF'}]}}\n",
			wantErr: "",
		},
		{
			name:    "vid wrong length",
			yaml:    images + "\nusb: {ip: {devices: [{vendorID: '105', productID: '0407'}]}}\n",
			wantErr: "must be 4 lowercase hex digits",
		},
		{
			name:    "shell metachar busid",
			yaml:    images + "\nusb: {ip: {devices: [{busid: '1-2;rm'}]}}\n",
			wantErr: "is not a valid Linux bus-id",
		},
		{
			name:    "bad server",
			yaml:    images + "\nusb: {ip: {server: 'host:'}}\n",
			wantErr: "usb.ip.server",
		},
		{
			name:    "duplicate busid same server",
			yaml:    images + "\nusb: {ip: {devices: [{busid: '1-2'}, {busid: '1-2'}]}}\n",
			wantErr: "duplicate of `usb.ip.devices[0]`",
		},
		{
			name:    "duplicate vid/pid same server",
			yaml:    images + "\nusb: {ip: {devices: [{vendorID: '1050', productID: '0407'}, {vendorID: '1050', productID: '0407'}]}}\n",
			wantErr: "duplicate of `usb.ip.devices[0]`",
		},
		{
			name:    "same busid on different servers is fine",
			yaml:    images + "\nusb: {ip: {devices: [{busid: '1-2', server: 'a:3240'}, {busid: '1-2', server: 'b:3240'}]}}\n",
			wantErr: "",
		},
		{
			// `host` vs `host:3240` are the same endpoint after
			// FillDefault; the duplicate-check key normalises so
			// they collapse to one entry.
			name:    "duplicate via default-port-equivalent server",
			yaml:    images + "\nusb: {ip: {server: 'host.lima.internal', devices: [{busid: '1-2'}, {busid: '1-2', server: 'host.lima.internal:3240'}]}}\n",
			wantErr: "duplicate of `usb.ip.devices[0]`",
		},
		{
			// An explicitly-empty devices list with no server is a
			// no-op and must validate cleanly. Pins behaviour against
			// a future refactor turning this into an error.
			name:    "empty devices list is a no-op",
			yaml:    images + "\nusb: {ip: {devices: []}}\n",
			wantErr: "",
		},
		{
			name:    "non-linux guest rejects usb.ip.devices",
			yaml:    images + "\nos: 'Windows'\nusb: {ip: {devices: [{busid: '1-2'}]}}\n",
			wantErr: "only supported for Linux guests",
		},
		{
			// S4: server-only usb.ip block on non-Linux is inert
			// and must be rejected at validate time.
			name:    "non-linux guest rejects server-only usb.ip",
			yaml:    images + "\nos: 'Windows'\nusb: {ip: {server: 'host.lima.internal:3240'}}\n",
			wantErr: "only supported for Linux guests",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			y, err := Load(t.Context(), []byte(tc.yaml), "usbip.yaml")
			assert.NilError(t, err)
			err = Validate(y, false)
			if tc.wantErr == "" {
				assert.NilError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
}

func TestFillDefaultUSBIP(t *testing.T) {
	t.Run("default server only materialized when devices configured", func(t *testing.T) {
		y, err := Load(t.Context(), []byte("images: [{location: '/'}]\n"), "none.yaml")
		assert.NilError(t, err)
		assert.Assert(t, y.USB.IP.Server == nil, "expected no default server when no devices are configured")
		assert.Equal(t, len(y.USB.IP.Devices), 0)
	})

	t.Run("top-level default populates and per-device inherits or overrides", func(t *testing.T) {
		y, err := Load(t.Context(), []byte("images: [{location: '/'}]\nusb: {ip: {devices: [{busid: '1-2'}, {busid: '4-1', server: 'other:9999'}]}}\n"), "usbip.yaml")
		assert.NilError(t, err)
		assert.Equal(t, len(y.USB.IP.Devices), 2)
		assert.Assert(t, y.USB.IP.Server != nil)
		assert.Equal(t, *y.USB.IP.Server, "host.lima.internal:3240")
		assert.Assert(t, y.USB.IP.Devices[0].Server != nil)
		assert.Equal(t, *y.USB.IP.Devices[0].Server, "host.lima.internal:3240")
		assert.Assert(t, y.USB.IP.Devices[1].Server != nil)
		assert.Equal(t, *y.USB.IP.Devices[1].Server, "other:9999")
	})

	t.Run("server pointer is copied not aliased", func(t *testing.T) {
		// Mutating the top-level Server after FillDefault must not affect
		// per-device Server pointers (regression guard for pointer aliasing).
		y, err := Load(t.Context(), []byte("images: [{location: '/'}]\nusb: {ip: {devices: [{busid: '1-2'}]}}\n"), "usbip.yaml")
		assert.NilError(t, err)
		*y.USB.IP.Server = "mutated"
		assert.Equal(t, *y.USB.IP.Devices[0].Server, "host.lima.internal:3240")
	})

	t.Run("hex VID/PID is canonicalised to lowercase", func(t *testing.T) {
		// Users often paste uppercase hex from `lsusb` output; the
		// validator only accepts lowercase, so FillDefault canonicalises
		// to keep the UX friendly.
		y, err := Load(t.Context(), []byte("images: [{location: '/'}]\nusb: {ip: {devices: [{vendorID: '1050', productID: '04FF'}]}}\n"), "usbip.yaml")
		assert.NilError(t, err)
		assert.Equal(t, len(y.USB.IP.Devices), 1)
		assert.Assert(t, y.USB.IP.Devices[0].VendorID != nil)
		assert.Assert(t, y.USB.IP.Devices[0].ProductID != nil)
		assert.Equal(t, *y.USB.IP.Devices[0].VendorID, "1050")
		assert.Equal(t, *y.USB.IP.Devices[0].ProductID, "04ff")
	})

	t.Run("FillDefault inheritance order: o overrides y overrides d", func(t *testing.T) {
		// Build y with a default-fillable server, then call FillDefault with d/o
		// layers directly to exercise the three-way merge.
		y := &limatype.LimaYAML{
			Images: []limatype.Image{{File: limatype.File{Location: "/"}}},
			USB: limatype.USB{IP: limatype.USBIP{
				Devices: []limatype.USBIPDevice{{BusID: "1-2"}},
			}},
		}
		d := &limatype.LimaYAML{USB: limatype.USB{IP: limatype.USBIP{Server: ptr.Of("from-default:1111")}}}
		o := &limatype.LimaYAML{USB: limatype.USB{IP: limatype.USBIP{Server: ptr.Of("from-override:2222")}}}
		FillDefault(t.Context(), y, d, o, "merge.yaml", false)
		assert.Assert(t, y.USB.IP.Server != nil)
		assert.Equal(t, *y.USB.IP.Server, "from-override:2222")
		assert.Equal(t, *y.USB.IP.Devices[0].Server, "from-override:2222")
	})

	t.Run("override devices are concatenated and inherit override server", func(t *testing.T) {
		y := &limatype.LimaYAML{
			Images: []limatype.Image{{File: limatype.File{Location: "/"}}},
			USB: limatype.USB{IP: limatype.USBIP{
				Devices: []limatype.USBIPDevice{{BusID: "2-1"}},
			}},
		}
		d := &limatype.LimaYAML{}
		o := &limatype.LimaYAML{USB: limatype.USB{IP: limatype.USBIP{
			Server:  ptr.Of("override:1234"),
			Devices: []limatype.USBIPDevice{{BusID: "3-1"}},
		}}}
		FillDefault(t.Context(), y, d, o, "merge.yaml", false)
		assert.Equal(t, len(y.USB.IP.Devices), 2)
		// Override devices come first (slices.Concat(o, y, d)).
		assert.Equal(t, y.USB.IP.Devices[0].BusID, "3-1")
		assert.Equal(t, y.USB.IP.Devices[1].BusID, "2-1")
		assert.Equal(t, *y.USB.IP.Devices[0].Server, "override:1234")
		assert.Equal(t, *y.USB.IP.Devices[1].Server, "override:1234")
	})
}

// TestValidateExperimentalUSBIPTemplate is a schema-sanity smoke for the
// experimental template. CI does not exercise templates/experimental/ in
// hack/test-templates.sh, so without this test a breaking change to the
// `usb.ip` shape could land without the shipped template noticing.
//
// It only asserts the YAML unmarshals into LimaYAML and that the usb.ip
// block survives parsing; full Validate would require resolving `base:`
// (network) and is out of scope for a unit test.
func TestValidateExperimentalUSBIPTemplate(t *testing.T) {
	b, err := os.ReadFile("../../templates/experimental/usb-ip.yaml")
	assert.NilError(t, err)
	// Only unmarshal the `usb:` subtree. Full LimaYAML.Unmarshal would
	// trip on `base:` (custom unmarshaler) without a Read+Embed pass.
	var y struct {
		USB limatype.USB `yaml:"usb"`
	}
	assert.NilError(t, yaml.Unmarshal(b, &y))
	assert.Assert(t, y.USB.IP.Server != nil, "template must set usb.ip.server")
	assert.Assert(t, len(y.USB.IP.Devices) > 0, "template must declare at least one usb.ip.device")
}
