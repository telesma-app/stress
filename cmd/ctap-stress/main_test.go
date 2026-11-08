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
		{Path: "100001717"},
	}
	for _, test := range []struct {
		selector string
		want     string
	}{
		{selector: "1", want: "/dev/hidraw1"},
		{selector: "/dev/hidraw2", want: "/dev/hidraw2"},
		{selector: "one", want: "/dev/hidraw1"},
		{selector: "100001717", want: "100001717"},
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

func TestSelectDeviceSerialState(t *testing.T) {
	t.Parallel()

	devices := []*ghid.DeviceInfo{
		{Path: "unknown", SerialNbr: ghid.Descriptor[string]{}},
		{Path: "failed", SerialNbr: ghid.Descriptor[string]{Err: errors.New("read failed")}},
		{Path: "empty", SerialNbr: ghid.Descriptor[string]{Value: new(string)}},
		{Path: "read", SerialNbr: ghid.Descriptor[string]{Value: new("one")}},
	}

	device, err := selectDevice(devices, "one")
	if err != nil || device != devices[3] {
		t.Fatalf("selectDevice(one) = %v, %v; want the read serial", device, err)
	}

	if _, err := selectDevice(devices[:3], "one"); err == nil {
		t.Fatal("selected an unread serial")
	}

	if _, err := selectDevice(devices, ""); err == nil {
		t.Fatal("empty selector selected a device from multiple endpoints")
	}

	device, err = selectDevice(devices, "3")
	if err != nil || device != devices[2] {
		t.Fatalf("selectDevice(3) = %v, %v; want the observed empty serial", device, err)
	}

	devices = append(devices, &ghid.DeviceInfo{
		Path: "duplicate", SerialNbr: ghid.Descriptor[string]{Value: new("one")},
	})
	if _, err := selectDevice(devices, "one"); err == nil {
		t.Fatal("ambiguous read serial accepted")
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

func TestHelpExitsSuccessfully(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"-h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("runCLI(-h) code = %d, want 0", code)
	}

	if help := stderr.String(); !strings.Contains(help, "-lock-seconds") || !strings.Contains(help, "ping,lock,get-info") {
		t.Fatalf("help does not describe lock support:\n%s", help)
	}
}
