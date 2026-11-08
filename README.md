# telesma-app/stress

[![Go](https://github.com/telesma-app/stress/actions/workflows/go.yml/badge.svg)](https://github.com/telesma-app/stress/actions/workflows/go.yml)

`telesma-app/stress` is a non-destructive stress test for USB CTAP2 authenticators. It sends CTAPHID and safe CTAP commands,
checks the replies, and reports errors, latency, and request rate.

The tool uses [`telesma-app/ctap`](https://github.com/telesma-app/ctap) and the cgo-free
[`telesma-app/hid`](https://github.com/telesma-app/hid) backend.

> [!WARNING]
> The test does not create, change, or delete authenticator data. The optional `lock` operation temporarily blocks other
> CTAPHID channels. A high load can still hang or restart a device with faulty firmware. Start with one worker and a short
> run.

## Features

| Operation | What it tests |
|---|---|
| `ping` | CTAPHID framing, fragmented messages, and full echo replies |
| `lock` | CTAPHID channel locking and contention between independent channels |
| `get-info` | CBOR decoding and stable capability fields from `authenticatorGetInfo` |
| `pin-retries` | Read-only `getPINRetries`; requires the reported `clientPin` option |
| `uv-retries` | Read-only `getUVRetries`; requires the reported `uv` option and CTAP 2.1 or newer |

Each worker opens its own HID handle and CTAPHID channel. This lets the test check how the firmware handles independent
channels at the same time.

## Installation

See [`go.mod`](go.mod) for the required Go version. Clone the repository and build
from its root:

```sh
git clone https://github.com/telesma-app/stress.git
cd stress
go build ./cmd/ctap-stress
```

The Go module path remains `github.com/go-ctap/stress`. Running the tool requires
a connected USB FIDO2 authenticator and OS permission to access its HID interface.

## Quick start

Run these commands from the repository root.

List available FIDO HID devices:

```sh
go run ./cmd/ctap-stress -list
```

Run 200 commands against the first device, using one worker:

```sh
go run ./cmd/ctap-stress \
  -device 1 \
  -workers 1 \
  -requests 200 \
  -duration 0
```

Then test several channels for five minutes:

```sh
go run ./cmd/ctap-stress \
  -device 1 \
  -workers 4 \
  -duration 5m \
  -rate 100 \
  -ops ping,get-info
```

`-device` accepts the number shown by `-list`, a HID path, or a unique serial number. If only one device is connected,
the option can be omitted. Only successfully read serial numbers participate in selection; use the ordinal or path
when the serial is empty or unavailable. A reported serial is not a verified device identity.

The device list and run preamble preserve descriptor read outcomes. Read IDs are hexadecimal, including `0000`;
read strings are quoted, including `""` for an observed empty string. `<unavailable>` means the platform did not
provide a value; `<error message="...">` reports a descriptor read failure. Neither outcome
establishes that the device has no descriptor.

## Test selection

The default run lasts 30 seconds with one worker and alternates between `ping` and `get-info`. Use separate runs to find
which layer fails:

```sh
# CTAPHID framing, fragmentation, and echo only.
go run ./cmd/ctap-stress -device 1 -duration 2m -ops ping

# CTAP2 and CBOR GetInfo only.
go run ./cmd/ctap-stress -device 1 -duration 2m -ops get-info

# Extra read-only commands, when the device reports support for them.
go run ./cmd/ctap-stress -device 1 -duration 2m \
  -ops get-info,pin-retries,uv-retries
```

The default ping payload sizes are `0,1,57,58,64,116,117,1024,7609`. They cover the boundaries between initial and
continuation HID packets, plus the largest CTAPHID payload.

Before the run, the tool reads `authenticatorGetInfo` and limits the default ping sizes to the device's effective
`maxMsgSize`. An explicit `-ping-sizes` value overrides this limit, which is useful for negative testing:

```sh
go run ./cmd/ctap-stress -device 1 -ops ping -ping-sizes 0,57,58,1024
```

To exercise `CTAPHID_LOCK`, use at least two workers so other channels contend with the lock holder:

```sh
go run ./cmd/ctap-stress -device 1 -workers 4 -duration 2m \
  -ops lock,ping -lock-seconds 1
```

`-lock-seconds` accepts `0..10` and defaults to `1`. A value of `0` sends an unlock request. LOCK has no CTAPHID
capability flag, so unsupported devices are tested directly and typically report `ERR_INVALID_CMD`.

## Limits and recovery

`-duration` and `-requests` are both upper limits. The run stops when it reaches the first limit. Set `-duration 0` when
you need an exact number of attempts.

The `-rate` option sets a global request-per-second limit for all workers. A value of `0` means no limit.

After a command error, a worker closes its channel and opens a new one. This keeps the run active while the first error
stays in the report. Use `-reopen-on-error=false` to keep the same channel.

The CTAPHID transport retries `ERR_CHANNEL_BUSY` until the per-command timeout. During a multi-worker lock test,
contention can therefore appear as increased latency or a timeout instead of an immediate channel-busy error. A
successful lock is not explicitly released by the runner and expires after the configured duration, at most 10 seconds.

Press `Ctrl-C` once to stop the run and print the collected report. Press it again if an operating system or device call
does not stop.

Run `go run ./cmd/ctap-stress -help` to see all options.

## Reports

The text report includes:

- successful and failed attempts for each operation;
- average, p50, p95, p99, and maximum latency;
- total request rate and ping traffic;
- channel open and close failures;
- error groups and a limited set of failure samples.

Use `-json` for a JSON report. Progress is written to standard error, so standard output can be saved directly:

```sh
go run ./cmd/ctap-stress -device 1 -duration 10m -json > report.json
```

The process uses these exit codes:

| Code | Meaning |
|---:|---|
| `0` | The run completed without device errors |
| `1` | The report contains command, open, or close failures |
| `2` | Discovery, preflight, configuration, or report output failed |

## Usage notes

- Do not run other programs that change the same authenticator during a test.
- Start with `-workers 1` and a small `-requests` value before adding load.
- Use `-ops ping` to focus on the HID and CTAPHID transport.
- Do not use the authenticator from another program during a lock test; its channel may be blocked for up to 10 seconds.
- An explicit ping size above `maxMsgSize` may correctly return `ERR_INVALID_LEN`; this is a negative test, not always a
  firmware bug.
- Percentiles are upper bounds from latency buckets, not exact sampled values.

## Development

Run tests and static checks with the Go version required by [`go.mod`](go.mod):

```sh
go test ./...
go vet ./...
```

Run golangci-lint v2 with the standard linters and `wsl_v5`:

```sh
golangci-lint run ./...
```

## License

[Apache License 2.0](LICENSE).
