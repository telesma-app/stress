# go-ctap/stress

`go-ctap/stress` is a read-only stress test for USB CTAP2 authenticators. It sends CTAPHID pings and safe CTAP commands,
checks the replies, and reports errors, latency, and request rate.

The tool uses [`go-ctap/ctap`](https://github.com/go-ctap/ctap) and the cgo-free
[`go-ctap/hid`](https://github.com/go-ctap/hid) backend.

> [!WARNING]
> The test does not create, change, or delete authenticator data. However, a high load can still hang or restart a device
> with faulty firmware. Start with one worker and a short run.

## Operations

| Operation | What it tests |
|---|---|
| `ping` | CTAPHID framing, fragmented messages, and full echo replies |
| `get-info` | CBOR decoding and stable capability fields from `authenticatorGetInfo` |
| `pin-retries` | Read-only `getPINRetries`; requires the reported `clientPin` option |
| `uv-retries` | Read-only `getUVRetries`; requires the reported `uv` option and CTAP 2.1 or newer |

Each worker opens its own HID handle and CTAPHID channel. This lets the test check how the firmware handles independent
channels at the same time.

## Quick start

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
the option can be omitted.

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

## Limits and recovery

`-duration` and `-requests` are both upper limits. The run stops when it reaches the first limit. Set `-duration 0` when
you need an exact number of attempts.

The `-rate` option sets a global request-per-second limit for all workers. A value of `0` means no limit.

After a command error, a worker closes its channel and opens a new one. This keeps the run active while the first error
stays in the report. Use `-reopen-on-error=false` to keep the same channel.

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
- An explicit ping size above `maxMsgSize` may correctly return `ERR_INVALID_LEN`; this is a negative test, not always a
  firmware bug.
- Percentiles are upper bounds from latency buckets, not exact sampled values.

## Testing

```sh
go test ./...
go vet ./...
```
