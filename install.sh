#!/usr/bin/env bash
set -euo pipefail

APP_DIR="/opt/wot-relay-manager"
ENV_FILE="/etc/wot-relay-manager.env"
SERVICE_FILE="/etc/systemd/system/wot-relay-manager.service"
USER_NAME="root"

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run this installer with sudo." >&2
  exit 1
fi

GO_BIN="${GO_BIN:-}"
if [[ -z "${GO_BIN}" ]]; then
  if command -v go >/dev/null 2>&1; then
    GO_BIN="$(command -v go)"
  elif [[ -x /usr/local/go/bin/go ]]; then
    GO_BIN="/usr/local/go/bin/go"
  elif [[ -x /usr/lib/go/bin/go ]]; then
    GO_BIN="/usr/lib/go/bin/go"
  elif [[ -x /root/go/bin/go ]]; then
    GO_BIN="/root/go/bin/go"
  fi
fi

if [[ -z "${GO_BIN}" || ! -x "${GO_BIN}" ]]; then
  echo "Go is required. If it is already installed, rerun with GO_BIN=/path/to/go sudo -E bash install.sh" >&2
  exit 1
fi

mkdir -p "${APP_DIR}"
cp -R . "${APP_DIR}/"
cd "${APP_DIR}"
chown -R root:root "${APP_DIR}"
find "${APP_DIR}" -name '._*' -delete

"${GO_BIN}" mod download
"${GO_BIN}" build -trimpath -ldflags="-s -w" -o wot-relay-manager .

if [[ ! -f "${ENV_FILE}" ]]; then
  GENERATED_PASSWORD="$(openssl rand -base64 18 | tr '+/' '-_' | tr -d '=')"
  PASSWORD_HASH="$(printf '%s' "${GENERATED_PASSWORD}" | sha256sum | awk '{print $1}')"
  cat > "${ENV_FILE}" <<'ENV'
BIND_ADDR=127.0.0.1:4781
RELAY_HTTP=http://127.0.0.1:3334
RELAY_WS=ws://127.0.0.1:3334
RELAY_ENV=/etc/systemd/system/wot-relay.env
SERVICE_NAME=wot-relay.service
MANAGER_USERNAME=relayadmin
ENV
  printf 'MANAGER_PASSWORD_SHA256=%s\n' "${PASSWORD_HASH}" >> "${ENV_FILE}"
  chmod 0600 "${ENV_FILE}"
else
  GENERATED_PASSWORD=""
fi

cat > "${SERVICE_FILE}" <<SERVICE
[Unit]
Description=WoT Relay Manager
After=network.target wot-relay.service

[Service]
Type=simple
User=${USER_NAME}
Group=${USER_NAME}
WorkingDirectory=${APP_DIR}
EnvironmentFile=${ENV_FILE}
ExecStart=${APP_DIR}/wot-relay-manager
Restart=on-failure
MemoryMax=160M
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
SERVICE

systemctl daemon-reload
systemctl enable --now wot-relay-manager.service

cat <<'DONE'
WoT Relay Manager is installed.

Open an SSH tunnel from your own machine:
  ssh -L 4781:127.0.0.1:4781 root@YOUR_VPS_IP

Then open:
  http://127.0.0.1:4781

DONE

if [[ -n "${GENERATED_PASSWORD}" ]]; then
  cat <<DONE

Generated login:
  Username: relayadmin
  Password: ${GENERATED_PASSWORD}

Save this password now. It is shown only once.
DONE
else
  cat <<DONE

Existing ${ENV_FILE} kept. Use the credentials already stored there.
DONE
fi
