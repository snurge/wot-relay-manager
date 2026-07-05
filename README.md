# WoT Relay Manager

Lightweight local-only management and analytics panel for `barrydeen/wot-relay` on Debian.

It runs as a separate service on `127.0.0.1:4781`, reads the relay debug endpoint on `127.0.0.1:3334`, edits `/etc/systemd/system/wot-relay.env`, restarts `wot-relay.service`, reads recent journal activity, and browses stored notes through the relay's local WebSocket.

## Features

- Relay activity metrics from `/debug/stats`
- Service status, memory, CPU, task count, and database disk usage
- Journal-derived activity chart with labeled sample and per-minute axes
- Stored note search by kind, author `npub1...`, and text
- Separate live feed for new events passing through the relay
- Relay environment editor with restart control

## Install

Upload this folder to the VPS, then run:

```sh
cd wot-relay-manager
sudo bash install.sh
```

From your local machine, open a tunnel:

```sh
ssh -L 4781:127.0.0.1:4781 root@YOUR_VPS_IP
```

Then visit:

```text
http://127.0.0.1:4781
```

The installer generates a fresh password and prints it once. The default username is:

```text
relayadmin
```

The password hash is stored in `/etc/wot-relay-manager.env`. Change the password by replacing `MANAGER_PASSWORD_SHA256` with:

```sh
printf 'new-password-here' | sha256sum
sudo systemctl restart wot-relay-manager.service
```

## Expected Relay Paths

The installer matches the Relay Runner guide defaults:

```text
Relay env: /etc/systemd/system/wot-relay.env
Relay service: wot-relay.service
Relay HTTP: http://127.0.0.1:3334
Relay WebSocket: ws://127.0.0.1:3334
```

If your VPS differs, edit `/etc/wot-relay-manager.env` and restart:

```sh
sudo systemctl restart wot-relay-manager.service
```

## Public Write and Allowlist Controls

The upstream relay currently does not implement a public write switch or private write allowlist in its environment settings. The dashboard includes fields for:

```text
PUBLIC_WRITE_RELAY
WRITE_ALLOWLIST
```

To make those controls active, apply `patches/wot-relay-write-policy.patch` to the relay repository, rebuild the relay, and restart `wot-relay.service`.

The allowlist expects 64-character hex pubkeys. Convert any `npub` values before saving them there.

Without the patch, those two values can be saved in the env file but the relay will ignore them. Native settings such as archival mode, archive reactions, refresh interval, and minimum followers work through the existing relay configuration.
