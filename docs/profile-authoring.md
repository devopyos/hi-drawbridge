# Profile Authoring Guide

This is the guide for adding or iterating on device profiles for `hi-drawbridge`.

A profile tells the bridge:

1. how to identify the device
2. which `hidraw` endpoint to talk to
3. how to decode battery data out of the returned HID frames

## Two Normal Workflows

There are two sane ways to work on profiles.

### Local Iteration

If you are still poking at a device and do not want to ship anything yet, put YAML files here:

- `~/.config/hi-drawbridge/profiles/`

That local overlay directory can also be overridden with:

- `HI_DRAWBRIDGE_profiles_dir`
- `--profiles-dir`

Local YAML files are loaded on top of the built-in catalog, so they can:

- add new profile IDs
- override built-in profile IDs while you experiment

### Shipped Support

If the profile is ready to live in the repo, add it under:

- [`internal/profile/profiles/`](../internal/profile/profiles/)

That is a real product change, not “just data,” so docs, tests, and packaging examples need to move with it.

## Runtime Model

`hi-drawbridge` publishes BatteryWatch companion data on the session bus.

- bus: `org.batterywatch.Companion`
- path: `/org/batterywatch/Companion`
- method: `GetDevices()`

This project does not integrate with UPower, and it does not install host integration files for you.

## Step-by-step: Add a Device

### 1. Identify Vendor and Product IDs

List the `hidraw` nodes:

```bash
ls /sys/class/hidraw/
```

Inspect the `uevent` data:

```bash
for d in /sys/class/hidraw/hidraw*/device/uevent; do
  echo "=== $d ==="
  cat "$d"
  echo
done
```

Look for something like:

```text
HID_ID=0003:00003434:0000D030
HID_NAME=Keychron M7
```

`HID_ID` is `bus:vendor:product` in hex. Use 4-digit lowercase vendor/product IDs in YAML.

If the device has more than one transport, capture each transport product ID and map it under `transport_product_ids`.

### 2. Inspect the Interfaces

A single device can expose multiple `hidraw` nodes. For each candidate:

```bash
cat /sys/class/hidraw/hidrawN/device/bInterfaceNumber 2>/dev/null || echo "N/A"
cat /sys/class/hidraw/hidrawN/device/uevent | grep HID_PHYS
hid-decode < /sys/class/hidraw/hidrawN/device/report_descriptor
```

Record:

- input report IDs
- feature report IDs
- report sizes
- interface number

You use that data to define:

- `query_endpoint`
- `wake_endpoint`

### 3. Choose `probe_path`

Supported values:

| Value | Behavior |
|---|---|
| `feature_or_interrupt` | Try feature reads first, then fallback to interrupt reads |
| `feature_only` | Use feature reads only |
| `interrupt_only` | Send output query and decode interrupt frames only |
| `passive` | Do not send queries, just decode unsolicited interrupt frames |

Important: do **not** assume `feature_or_interrupt` is automatically safer.

Some devices or receivers emit interrupt frames that look structurally valid enough to pass shallow matching but are not real battery data. In that case, `feature_or_interrupt` can lie while `feature_only` will fail loudly. Failing loudly is better.

So the practical rule is:

- use `feature_only` when the feature path is real and the interrupt fallback is noisy, ack-like, or otherwise suspicious
- use `feature_or_interrupt` only when you have evidence that the fallback interrupt frame actually carries trustworthy battery state
- use `interrupt_only` only when the device genuinely does not expose a usable feature path

### 4. Determine the Frame Layout

You need these protocol details:

- feature query report ID and commands
- expected signature bytes in the feature response
- battery and status offsets in the feature frame
- optional interrupt fallback report ID/cmd/length/offset
- optional charging status byte values

Useful tools:

- `hi-drawbridge --profile my-device probe-debug`
- `hid-recorder`
- `evtest`
- vendor tools, if they exist

If the device starts returning nonsense, save the full `probe-debug` output before reconnecting anything. That is usually the difference between “we have a clue” and “we are guessing.”

### 4.5. Check `hidraw` Permissions

The profile is not enough if the user cannot open the relevant `hidraw` node.

If the device needs an access rule:

- add an example rule under [`packaging/udev-rules/`](../packaging/udev-rules/)
- update [`docs/manual-setup.md`](./manual-setup.md)

Good defaults:

- match concrete `idVendor` and `idProduct`
- add one rule line per transport product ID if needed
- prefer `TAG+="uaccess"` over broad world-readable modes

### 5. Write the YAML

For local iteration, create a file such as:

- `~/.config/hi-drawbridge/profiles/my-device.yaml`

For shipped support, create:

- `internal/profile/profiles/my-device.yaml`

Example:

```yaml
id: my-device
name: My Device Name
vendor_id: abcd
transport_product_ids:
  usb_direct: "1234"
  receiver: "5678"

device_type: mouse
icon_name: input-mouse
probe_path: feature_only

wake_report_hex: 00b200000000000000000000000000000000000000000000000000000000000000

query_report_id: 81
prime_query_cmd: 7
battery_query_cmd: 6
query_length: 21
expected_signature_hex: abcd1234
battery_offset: 11
status_offset: 12

fallback_input_report_id: 84
fallback_input_cmd: 228
fallback_input_length: 21
fallback_battery_offset: 2
fallback_battery_bucket_max: 4
charging_status_bytes: [1]

query_endpoint:
  required_feature_report_ids: [81]
wake_endpoint:
  interface_numbers: [3]
```

Notes:

- each file defines exactly one profile
- built-in profiles come from `internal/profile/profiles/*.yaml`
- local overlays come from `~/.config/hi-drawbridge/profiles/*.yaml` unless overridden
- `prime_query_cmd` is optional
- `fallback_battery_bucket_max: 0` means the fallback byte is already `0..100`
- fallback fields can exist even if `probe_path` is `feature_only`, but if the fallback path is untrustworthy, do not enable it just because the fields are known

### 6. Validate With the CLI

Start with full diagnostics:

```bash
hi-drawbridge --profile my-device probe-debug
```

If the profile YAML lives outside the default local overlay directory:

```bash
hi-drawbridge --profiles-dir /path/to/profiles --profile my-device probe-debug
```

Quick output check:

```bash
hi-drawbridge --profile my-device probe --require-data
```

Inspect the merged runtime state:

```bash
hi-drawbridge config
```

When a device goes weird, the most useful fields in `probe-debug` are:

- `frame_source`
- `candidate_path`
- `last_frame_hex`
- `best_readings`
- `discovery_diagnostics`

## YAML Field Reference

### Profile fields

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Unique lowercase profile ID used by `--profile`. |
| `name` | string | yes | Human-readable device name. |
| `vendor_id` | string | yes | 4-digit hex vendor ID. |
| `transport_product_ids` | map | yes | Transport to 4-digit product ID (`usb_direct`, `receiver`). |
| `device_type` | string | yes | Device class in the output payload, for example `mouse`. |
| `icon_name` | string | yes | Freedesktop icon name. |
| `probe_path` | string | yes | One of `feature_or_interrupt`, `feature_only`, `interrupt_only`, `passive`. |
| `wake_report_hex` | string | yes | Non-empty hex bytes written before probing. Max decoded size: 256 bytes. |
| `query_report_id` | int | yes | Feature report ID. Range: `0..255`. |
| `prime_query_cmd` | int | no | Optional setup command. Range: `0..255`. |
| `battery_query_cmd` | int | yes | Battery query command byte. Range: `0..255`. |
| `query_length` | int | yes | Feature/output frame length. `feature_only`/`feature_or_interrupt` >= 2, `interrupt_only` >= 3, `passive` >= 1, always <= 256. |
| `expected_signature_hex` | string | yes | Non-empty signature expected in the feature frame. Max decoded size: 64 bytes and must fit inside `query_length`. |
| `battery_offset` | int | yes | Battery byte offset in the feature frame. |
| `status_offset` | int | yes | Charging status byte offset in the feature frame. |
| `fallback_input_report_id` | int | yes | Interrupt fallback report ID. |
| `fallback_input_cmd` | int | yes | Expected command byte at offset 1 in the interrupt fallback frame. |
| `fallback_input_length` | int | yes | Minimum fallback frame length. |
| `fallback_battery_offset` | int | yes | Battery byte offset in the interrupt fallback frame. |
| `fallback_battery_bucket_max` | int | yes | Bucket max for fallback conversion. `0` means direct percentage. |
| `charging_status_bytes` | int array | no | Status byte values interpreted as charging. |
| `query_endpoint` | map | yes | Endpoint selector for the query node. |
| `wake_endpoint` | map | no | Endpoint selector for the wake node. Defaults to the query endpoint when omitted. |

### Endpoint selector fields

| Field | Type | Required | Description |
|---|---|---|---|
| `interface_numbers` | int array | no | Allowed USB interface numbers. |
| `required_input_report_ids` | int array | no | Input report IDs that must be present. |
| `required_feature_report_ids` | int array | no | Feature report IDs that must be present. |

All configured selector constraints must match for an endpoint to be selected.

`interface_numbers` is best-effort. Missing or malformed `bInterfaceNumber` data does not hard-fail discovery.

## Validation Rules

- required fields must be present
- unknown YAML keys are rejected
- only one YAML document is accepted per profile file
- `query_length` must be <= `256`
- `wake_report_hex` must decode to `1..256` bytes
- `expected_signature_hex` must decode to `1..64` bytes
- `transport_product_ids` must not contain duplicate product IDs within one profile

## Practical Tips

- start with `probe-debug`, not `probe`
- keep `expected_signature_hex` specific enough to avoid false positives
- do not trust interrupt fallback just because it decodes to *something*
- if a local profile shadows a built-in ID such as `keychron_m7`, the local YAML wins
- if a device suddenly starts lying and a reconnect fixes it, the problem was probably receiver or HID state, not your YAML
