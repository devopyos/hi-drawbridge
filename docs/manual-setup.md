# Manual Setup

`hi-drawbridge` does not install host integration for you.

That is deliberate. The binary probes devices and serves D-Bus data. Systemd and `udev` are your machine, so you wire that part up yourself.

The checked-in examples live under [`packaging/`](../packaging/).

## What You Actually Need

Usually that means two things:

- a user systemd service so the bridge starts with your session
- a `udev` rule if your desktop user cannot open the right `hidraw` node

If you are only running one-off `probe` commands by hand, you may not need the systemd part at all.

## Systemd User Service

Copy the example unit into your user systemd directory:

```bash
mkdir -p ~/.config/systemd/user
cp packaging/systemd-user/hi-drawbridge.service ~/.config/systemd/user/
```

The shipped unit assumes the binary lives at `%h/bin/hi-drawbridge`.

If yours lives somewhere else, edit `ExecStart=` before enabling it.

Reload and start it:

```bash
systemctl --user daemon-reload
systemctl --user enable --now hi-drawbridge.service
```

Verify it:

```bash
systemctl --user status hi-drawbridge.service
journalctl --user -u hi-drawbridge.service -n 50 --no-pager
gdbus call --session \
  --dest org.batterywatch.Companion \
  --object-path /org/batterywatch/Companion \
  --method org.batterywatch.Companion.GetDevices
```

If the D-Bus call returns a JSON string, the bridge is up and BatteryWatch should have something to consume.

## Udev Rules

If your user cannot access the needed `hidraw` node, install a matching `udev` rule.

The repo stores example rules under [`packaging/udev-rules/`](../packaging/udev-rules/). For the shipped Keychron M7 profile:

```bash
sudo cp packaging/udev-rules/99-hi-drawbridge-keychron-m7.rules /etc/udev/rules.d/
sudo udevadm control --reload-rules
sudo udevadm trigger
```

Then unplug and reconnect the device or receiver if needed.

The shipped rules use `TAG+="uaccess"` so the active desktop user gets access without making the device world-readable.
They are intentionally narrow: product-specific `hidraw` access only.

The repo does **not** ship these by default:

- broad vendor-wide rules like `ATTRS{idVendor}=="3434"` with no product match
- DFU / bootloader access rules for firmware tools
- runtime autosuspend tweaks

Those can be useful on your machine, but they are more invasive than the base access rule and do not belong in the default example unless the project proves they are required.

Useful checks:

```bash
ls -l /dev/hidraw*
udevadm info --attribute-walk --name=/dev/hidraw0
```

If the device still behaves weirdly after the rule is in place, replugging the receiver is sometimes the real fix. Some HID receivers get into a bad state and stop answering feature reads cleanly.
If replugging keeps being necessary, a local autosuspend workaround may help on some systems, but treat that as a host-specific workaround, not the default `hi-drawbridge` rule.

## For New Profiles

If you add a new shipped profile under [`internal/profile/profiles/`](../internal/profile/profiles/), add or update a matching example rule when the device needs `hidraw` permission changes.

Good defaults:

- keep example rules in `packaging/udev-rules/`
- use one file per device or closely related family
- match concrete `idVendor` and `idProduct`
- add one line per transport product ID when needed
- prefer `TAG+="uaccess"` over broad `MODE="0666"` style rules

Keep the rule example and the profile YAML aligned. If vendor/product IDs change in the profile, update the matching rule in the same change instead of leaving the repo half-right.
