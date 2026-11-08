package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/go-ctap/stress/internal/stress"
	"github.com/telesma-app/ctap/authenticator"
	hidbackend "github.com/telesma-app/ctap/backend/hid"
	"github.com/telesma-app/ctap/backend/hidproxy"
	"github.com/telesma-app/ctap/protocol"
	ghid "github.com/telesma-app/hid"
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	config := stress.DefaultConfig()
	flags := flag.NewFlagSet("ctap-stress", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		selector          string
		operations        string
		pingSizes         string
		listOnly          bool
		namedPipe         bool
		jsonOutput        bool
		pingSizesExplicit bool
	)
	flags.StringVar(&selector, "device", "", "device ordinal, HID path, or unique serial number")
	flags.BoolVar(&listOnly, "list", false, "list FIDO devices and exit")
	flags.BoolVar(&namedPipe, "named-pipe", false, "use the Windows CTAPHID named-pipe proxy")
	flags.DurationVar(&config.Duration, "duration", config.Duration, "maximum run duration; 0 disables the time limit")
	flags.Uint64Var(&config.Requests, "requests", config.Requests, "maximum total command attempts; 0 disables the count limit")
	flags.IntVar(&config.Workers, "workers", config.Workers, "parallel workers (one CTAPHID channel each)")
	flags.DurationVar(&config.CommandTimeout, "timeout", config.CommandTimeout, "per-command timeout")
	flags.DurationVar(&config.OpenTimeout, "open-timeout", config.OpenTimeout, "per-channel open timeout")
	flags.Float64Var(&config.Rate, "rate", config.Rate, "global requests/second limit; 0 means unlimited")
	flags.DurationVar(&config.ReopenDelay, "reopen-delay", config.ReopenDelay, "delay before reopening a failed channel")
	flags.BoolVar(&config.ReopenOnError, "reopen-on-error", config.ReopenOnError, "reopen a worker channel after a command failure")
	flags.BoolVar(&config.CheckInfoStable, "check-info-stability", config.CheckInfoStable, "fail if stable GetInfo capability fields drift")
	flags.DurationVar(&config.ProgressEvery, "progress", config.ProgressEvery, "progress interval; 0 disables progress")
	flags.IntVar(&config.FailureSamples, "failure-samples", config.FailureSamples, "maximum detailed failures retained")
	flags.BoolVar(&jsonOutput, "json", false, "write the final report as JSON")
	flags.StringVar(&operations, "ops", joinOperations(config.Operations), "comma-separated: ping,get-info,pin-retries,uv-retries")
	flags.StringVar(&pingSizes, "ping-sizes", joinInts(config.PingSizes), "comma-separated CTAPHID ping payload sizes (0..7609); explicit values override the GetInfo limit")

	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Read-only CTAP/CTAPHID stress runner")
		_, _ = fmt.Fprintf(stderr, "\nUsage: %s [flags]\n\n", flags.Name())
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}

	flags.Visit(func(setFlag *flag.Flag) {
		if setFlag.Name == "ping-sizes" {
			pingSizesExplicit = true
		}
	})

	var err error

	config.Operations, err = stress.ParseOperations(operations)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid -ops: %v\n", err)
		return 2
	}

	config.PingSizes, err = stress.ParsePingSizes(pingSizes)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid -ping-sizes: %v\n", err)
		return 2
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Restore the default signal behavior after the first interrupt.  The first
	// Ctrl-C requests a graceful report; a second one can then terminate a stuck
	// OS or device call immediately.
	go func() {
		<-rootCtx.Done()
		stop()
	}()

	discoveryCtx, cancelDiscovery := context.WithTimeout(rootCtx, config.OpenTimeout)
	devices, err := enumerateDevices(discoveryCtx, namedPipe)

	cancelDiscovery()

	if err != nil {
		_, _ = fmt.Fprintf(stderr, "device discovery failed: %v\n", err)
		return 2
	}

	if listOnly {
		printDevices(stdout, devices)
		return 0
	}

	selected, err := selectDevice(devices, selector)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		printDevices(stderr, devices)

		return 2
	}

	config.DevicePath = selected.Path
	if err := config.Validate(); err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 2
	}

	open := authenticatorOpener(namedPipe)
	preflightCtx, cancelPreflight := context.WithTimeout(rootCtx, config.OpenTimeout)
	preflight, err := open(preflightCtx, config.DevicePath)

	cancelPreflight()

	if err != nil {
		_, _ = fmt.Fprintf(stderr, "preflight open failed: %v\n", err)
		return 2
	}

	info, ok := preflight.GetInfoCached()
	if !ok {
		closeErr := preflight.Close()
		_, _ = fmt.Fprintf(stderr, "preflight GetInfo cache is unexpectedly invalid (close: %v)\n", closeErr)

		return 2
	}

	if err := preflight.Close(); err != nil {
		_, _ = fmt.Fprintf(stderr, "preflight close failed: %v\n", err)
		return 2
	}

	if slices.Contains(config.Operations, stress.OperationPing) {
		if pingSizesExplicit {
			if slices.ContainsFunc(config.PingSizes, func(size int) bool {
				return uint(size) > info.EffectiveMaxMsgSize()
			}) {
				_, _ = fmt.Fprintf(
					stderr,
					"explicit ping sizes override authenticator maxMsgSize=%d: %v\n",
					info.EffectiveMaxMsgSize(),
					config.PingSizes,
				)
			}
		} else if capped := stress.CapPingSizes(config.PingSizes, info.EffectiveMaxMsgSize()); !slices.Equal(config.PingSizes, capped) {
			config.PingSizes = capped
			_, _ = fmt.Fprintf(
				stderr,
				"ping sizes capped by authenticator maxMsgSize=%d: %v\n",
				info.EffectiveMaxMsgSize(),
				config.PingSizes,
			)
		}
	}

	if err := config.ValidateCapabilities(info); err != nil {
		_, _ = fmt.Fprintf(stderr, "unsupported configuration: %v\n", err)
		return 2
	}

	baseline := stress.SnapshotInfo(info)
	printPreamble(stderr, selected, info, config)
	runner := stress.Runner{
		Config:       config,
		Open:         open,
		BaselineInfo: baseline,
		Progress: func(snapshot stress.Snapshot) {
			_, _ = fmt.Fprintf(
				stderr,
				"elapsed=%s attempts=%d ok=%d failed=%d rate=%.1f/s\n",
				snapshot.Elapsed.Round(time.Millisecond),
				snapshot.Attempts,
				snapshot.Succeeded,
				snapshot.Failed,
				snapshot.RequestsPerSecond,
			)
		},
	}
	report, runErr := runner.Run(rootCtx)

	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(report); err != nil {
			_, _ = fmt.Fprintf(stderr, "encode report: %v\n", err)
			return 2
		}
	} else {
		printReport(stdout, report)
	}

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		_, _ = fmt.Fprintf(stderr, "run stopped: %v\n", runErr)
		return 2
	}

	if report.Failed != 0 || report.OpenFailures != 0 || report.CloseFailures != 0 {
		return 1
	}

	return 0
}

func authenticatorOpener(namedPipe bool) func(context.Context, string) (stress.Device, error) {
	open := hidbackend.Open
	if namedPipe {
		open = hidproxy.Open
	}

	return func(ctx context.Context, path string) (stress.Device, error) {
		transport, err := open(ctx, path)
		if err != nil {
			return nil, err
		}

		device, err := authenticator.New(ctx, transport)
		if err != nil {
			return nil, errors.Join(err, transport.Close())
		}

		return device, nil
	}
}

func enumerateDevices(ctx context.Context, namedPipe bool) ([]*ghid.DeviceInfo, error) {
	enumerate := hidbackend.Devices
	if namedPipe {
		enumerate = hidproxy.Devices
	}

	var devices []*ghid.DeviceInfo

	for device, err := range enumerate(ctx) {
		if err != nil {
			return nil, err
		}

		devices = append(devices, device)
	}

	return devices, nil
}

func selectDevice(devices []*ghid.DeviceInfo, selector string) (*ghid.DeviceInfo, error) {
	if len(devices) == 0 {
		return nil, fmt.Errorf("no FIDO HID devices found")
	}

	if selector == "" {
		if len(devices) == 1 {
			return devices[0], nil
		}

		return nil, fmt.Errorf("multiple devices found; select one with -device")
	}

	if ordinal, err := strconv.Atoi(selector); err == nil {
		if ordinal >= 1 && ordinal <= len(devices) {
			return devices[ordinal-1], nil
		}

		return nil, fmt.Errorf("device ordinal %d is outside 1..%d", ordinal, len(devices))
	}

	var matches []*ghid.DeviceInfo

	for _, device := range devices {
		if device.Path == selector || (device.SerialNbr.Value != nil && *device.SerialNbr.Value == selector) {
			matches = append(matches, device)
		}
	}

	if len(matches) == 1 {
		return matches[0], nil
	}

	if len(matches) > 1 {
		return nil, fmt.Errorf("device selector %q is ambiguous", selector)
	}

	return nil, fmt.Errorf("device %q was not found", selector)
}

func printDevices(w io.Writer, devices []*ghid.DeviceInfo) {
	if len(devices) == 0 {
		_, _ = fmt.Fprintln(w, "No FIDO HID devices found.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)

	_, _ = fmt.Fprintln(tw, "#\tVID:PID\tPRODUCT\tSERIAL\tPATH")
	for index, device := range devices {
		_, _ = fmt.Fprintf(
			tw,
			"%d\t%s:%s\t%s\t%s\t%s\n",
			index+1,
			formatDescriptor(device.VendorID, "%04x"),
			formatDescriptor(device.ProductID, "%04x"),
			formatDescriptor(device.ProductStr, "%q"),
			formatDescriptor(device.SerialNbr, "%q"),
			device.Path,
		)
	}

	_ = tw.Flush()
}

func printPreamble(w io.Writer, device *ghid.DeviceInfo, info protocol.AuthenticatorGetInfoResponse, config stress.Config) {
	_, _ = fmt.Fprintf(w, "device: %s (%s:%s), path=%s\n",
		formatDescriptor(device.ProductStr, "%q"),
		formatDescriptor(device.VendorID, "%04x"),
		formatDescriptor(device.ProductID, "%04x"),
		device.Path,
	)
	_, _ = fmt.Fprintf(w, "CTAP versions: %v, AAGUID: %s, maxMsgSize: %d\n", info.Versions, info.AAGUID, info.EffectiveMaxMsgSize())
	_, _ = fmt.Fprintf(w, "load: workers=%d ops=%s timeout=%s duration=%s requests=%d\n", config.Workers, joinOperations(config.Operations), config.CommandTimeout, config.Duration, config.Requests)
	_, _ = fmt.Fprintln(w, "suite is read-only; Ctrl-C stops it and still prints the collected report")
}

func printReport(w io.Writer, report stress.Report) {
	_, _ = fmt.Fprintf(
		w,
		"\nResult: elapsed=%s attempts=%d ok=%d failed=%d rate=%.1f/s opens=%d open_failures=%d close_failures=%d\n",
		report.Elapsed.Round(time.Millisecond),
		report.Attempts,
		report.Succeeded,
		report.Failed,
		report.RequestsPerSecond,
		report.Opens,
		report.OpenFailures,
		report.CloseFailures,
	)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "OPERATION\tATTEMPTS\tOK\tFAILED\tAVG\tP50<=\tP95<=\tP99<=\tMAX\tPING BYTES")

	for _, operation := range report.SortedOperations() {
		stats := report.Operations[operation]
		_, _ = fmt.Fprintf(
			tw,
			"%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%d\n",
			operation,
			stats.Attempts,
			stats.Succeeded,
			stats.Failed,
			stats.Average.Round(time.Microsecond),
			stats.P50,
			stats.P95,
			stats.P99,
			stats.Max.Round(time.Microsecond),
			stats.PingBytes,
		)
	}

	_ = tw.Flush()

	if len(report.FailureKinds) != 0 {
		_, _ = fmt.Fprintln(w, "Failure kinds:")

		kinds := make([]string, 0, len(report.FailureKinds))
		for kind := range report.FailureKinds {
			kinds = append(kinds, kind)
		}

		slices.Sort(kinds)

		for _, kind := range kinds {
			count := report.FailureKinds[kind]
			_, _ = fmt.Fprintf(w, "  %s: %d\n", kind, count)
		}
	}

	if len(report.FailureSamples) != 0 {
		_, _ = fmt.Fprintln(w, "Failure samples:")

		for _, sample := range report.FailureSamples {
			pingSize := ""
			if sample.PingBytes != nil {
				pingSize = fmt.Sprintf(" bytes=%d", *sample.PingBytes)
			}

			_, _ = fmt.Fprintf(w, "  %s worker=%d op=%s%s kind=%s: %s\n", sample.At.Format(time.RFC3339Nano), sample.Worker, sample.Operation, pingSize, sample.Kind, sample.Error)
		}
	}
}

func joinOperations(operations []stress.Operation) string {
	parts := make([]string, len(operations))
	for index, operation := range operations {
		parts[index] = string(operation)
	}

	return strings.Join(parts, ",")
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(value)
	}

	return strings.Join(parts, ",")
}

func formatDescriptor[T any](descriptor ghid.Descriptor[T], format string) string {
	if descriptor.Err != nil {
		return fmt.Sprintf("<error message=%q>", descriptor.Err.Error())
	}

	if descriptor.Value == nil {
		return "<unavailable>"
	}

	return fmt.Sprintf(format, *descriptor.Value)
}
