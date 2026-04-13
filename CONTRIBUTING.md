# Contributing

If you want to help, great!

## Before You Touch Anything

You will want:

- Linux
- Go `1.26.2`
- actual access to `hidraw` devices if you are testing against real hardware
- a bit of patience if you are reverse engineering vendor HID weirdness

If you are only fixing docs, config parsing, or internal logic, you do not need physical hardware.

## Build

```bash
go build -o bin/hi-drawbridge ./cmd/hi-drawbridge
```

## Validation

This is the baseline. If your change affects behavior, this should be green before you call it done:

```bash
golangci-lint fmt --diff
golangci-lint run ./...
go test ./...
go test -race ./...
go vet ./...
go test -cover ./...
```

While iterating, package-local tests are fine. Before the change is ready, run the full set. CI enforces the same general standard, so if it is red there, it is not done here either.

## Repo Map

- `cmd/hi-drawbridge/`: binary entrypoint
- `internal/cli/`: Cobra commands and runtime wiring
- `internal/profile/`: embedded profile catalog, local overlay loading, validation, and selection
- `internal/*`: discovery, probing, polling, D-Bus export, bridge logic, and related runtime code
- `packaging/`: checked-in example systemd and `udev` assets
- `docs/`: manual setup and profile authoring docs

## Common Change Types

Most changes fall into one of these buckets:

- profile/catalog changes
- runtime/app code changes
- CLI/output changes
- docs/packaging changes

If your change is real, it is usually more than one bucket.

## Profile Changes

Shipped device support is catalog-driven, so profile changes are not “just data.” They are product changes.

If you are only experimenting locally, do not start by editing the embedded catalog unless you actually mean to ship the change.

Local profile overlays live here by default:

- `~/.config/hi-drawbridge/profiles`

You can also point the CLI at a different overlay directory with:

- `HI_DRAWBRIDGE_profiles_dir`
- `--profiles-dir`

If you add or change a profile:

1. Edit or add one YAML file under `internal/profile/profiles/`.
2. Validate with `probe-debug` if you can test on real hardware.
3. Update [`docs/profile-authoring.md`](docs/profile-authoring.md) if the authoring contract changed.
4. Add or update a matching example `udev` rule under `packaging/udev-rules/` if the device needs `hidraw` permission changes.
5. Update [`docs/manual-setup.md`](docs/manual-setup.md) and [`README.md`](README.md) if the user-facing setup story changed.

If you are not shipping the change yet, put the YAML in your local overlay directory instead and keep the repo clean.

The binary does not install host integration for the user. The checked-in docs and packaging examples are the source of truth for that part.

Do not assume `feature_or_interrupt` is automatically the right answer, either. If the interrupt path is noisy or ack-like, `feature_only` is safer because failing is better than lying.

## Runtime / App Code Changes

When changing runtime behavior, prefer boring and predictable over clever.

That means:

- deterministic output beats whatever map iteration happened to do
- explicit validation beats silent fallback
- race-safe behavior is not optional
- source-attributed errors are better than vague “something failed”
- if behavior changes, tests should move with it
- if a refactor makes the code shorter but weaker, that is not an improvement.

## CLI Changes

CLI output is part of the contract.

- Machine-readable output stays on stdout.
- Diagnostics and errors stay off stdout.
- JSON output should use typed structs where practical, not ad hoc maps.
- If command behavior changes, update the tests under `internal/cli/`.
- If you change the JSON shape, do it intentionally and call it out clearly.

## Docs and Packaging

This repo treats host integration as documentation and packaging, not as something the binary does for you.

So if setup changes, the docs and examples need to change too.

Relevant places:

- [`README.md`](README.md)
- [`docs/manual-setup.md`](docs/manual-setup.md)
- [`docs/profile-authoring.md`](docs/profile-authoring.md)
- [`packaging/systemd-user/`](packaging/systemd-user)
- [`packaging/udev-rules/`](packaging/udev-rules)

If those are now lying to the user after your change, the change is incomplete.

## Review Expectations

Good changes usually have these properties:

- deterministic behavior
- explicit validation
- race-safe logic
- tests that cover the actual contract
- docs that still match reality

If your change weakens one of those, explain why in plain language. “It was easier this way” is not a serious reason tho.

## Useful Contributions

Things that are genuinely useful here:

- new device profiles
- cleaner probe/debug output
- HID descriptor analysis
- better docs where setup or authoring is confusing
- permission / `udev` fixes
- tests for behavior that currently relies too much on luck

If you are reporting a bug, the most useful things you can include are:

- device name
- vendor/product IDs
- relevant `probe-debug` output
- HID descriptor details if you have them
- what you expected vs what actually happened

That'll save a lot of guessing, I guess.
