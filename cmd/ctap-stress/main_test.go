package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/go-ctap/stress/internal/stress"
	"github.com/telesma-app/ctap/protocol"
	ghid "github.com/telesma-app/hid"
)

func TestSelectDevice(t *testing.T) {
	t.Parallel()

	devices := []*ghid.DeviceInfo{
		{Path: "/dev/hidraw1", SerialNbr: ghid.Descriptor[string]{Value: new("one")}},
		{Path: "/dev/hidraw2", SerialNbr: ghid.Descriptor[string]{Value: new("two")}},
	}
	for _, test := range []struct {
		selector string
		want     string
	}{
		{selector: "1", want: "/dev/hidraw1"},
		{selector: "/dev/hidraw2", want: "/dev/hidraw2"},
		{selector: "one", want: "/dev/hidraw1"},
	} {
		device, err := selectDevice(devices, test.selector)
		if err != nil {
			t.Fatalf("selectDevice(%q): %v", test.selector, err)
		}

		if device.Path != test.want {
			t.Fatalf("selectDevice(%q) = %q, want %q", test.selector, device.Path, test.want)
		}
	}

	if _, err := selectDevice(devices, ""); err == nil {
		t.Fatal("empty selector accepted with multiple devices")
	}

	if _, err := selectDevice(devices, "missing"); err == nil {
		t.Fatal("missing selector accepted")
	}
}

func TestHelpExitsSuccessfully(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"-h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("runCLI(-h) code = %d, want 0", code)
	}
}

func TestPrintDeviceDescriptors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		device  ghid.DeviceInfo
		id      string
		product string
		serial  string
	}{
		{
			name: "observed zeros and empty strings",
			device: ghid.DeviceInfo{
				VendorID:   ghid.Descriptor[uint16]{Value: new(uint16)},
				ProductID:  ghid.Descriptor[uint16]{Value: new(uint16)},
				ProductStr: ghid.Descriptor[string]{Value: new(string)},
				SerialNbr:  ghid.Descriptor[string]{Value: new(string)},
			},
			id: "0000:0000", product: `""`, serial: `""`,
		},
		{
			name: "unavailable",
			id:   "<unavailable>:<unavailable>", product: "<unavailable>", serial: "<unavailable>",
		},
		{
			name: "read failures",
			device: ghid.DeviceInfo{
				VendorID:   ghid.Descriptor[uint16]{Err: errors.New("vid failure")},
				ProductID:  ghid.Descriptor[uint16]{Err: errors.New("pid failure")},
				ProductStr: ghid.Descriptor[string]{Err: errors.New("product failure")},
				SerialNbr:  ghid.Descriptor[string]{Err: errors.New("serial\nfailure")},
			},
			id:      `<error message="vid failure">:<error message="pid failure">`,
			product: `<error message="product failure">`,
			serial:  `<error message="serial\nfailure">`,
		},
		{
			name: "quoted values",
			device: ghid.DeviceInfo{
				VendorID:   ghid.Descriptor[uint16]{Value: new(uint16(0x1050))},
				ProductID:  ghid.Descriptor[uint16]{Value: new(uint16(0x0406))},
				ProductStr: ghid.Descriptor[string]{Value: new("<unavailable>")},
				SerialNbr:  ghid.Descriptor[string]{Value: new("one\ttwo\nthree")},
			},
			id: "1050:0406", product: `"<unavailable>"`, serial: `"one\ttwo\nthree"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.device.Path = "test-path"

			var list, preamble bytes.Buffer
			printDevices(&list, []*ghid.DeviceInfo{&test.device})

			lines := strings.Split(list.String(), "\n")
			if len(lines) != 3 || lines[2] != "" {
				t.Fatalf("device list contains unexpected line breaks:\n%s", &list)
			}

			want := "1 " + test.id + " " + test.product + " " + test.serial + " test-path"
			if got := strings.Join(strings.Fields(lines[1]), " "); got != want {
				t.Errorf("device row = %q, want %q", got, want)
			}

			printPreamble(&preamble, &test.device, protocol.AuthenticatorGetInfoResponse{}, stress.DefaultConfig())

			want = "device: " + test.product + " (" + test.id + "), path=test-path\n"
			if !strings.HasPrefix(preamble.String(), want) {
				t.Errorf("preamble = %q, want prefix %q", preamble.String(), want)
			}
		})
	}
}
