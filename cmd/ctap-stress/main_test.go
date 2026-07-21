package main

import (
	"bytes"
	"testing"

	ghid "github.com/go-ctap/hid"
)

func TestSelectDevice(t *testing.T) {
	t.Parallel()

	devices := []*ghid.DeviceInfo{
		{Path: "/dev/hidraw1", SerialNbr: "one"},
		{Path: "/dev/hidraw2", SerialNbr: "two"},
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
