#!/usr/bin/env bash
# Panel install helper for Linux (master + agent).
# Usage:
#   sudo ./install.sh master  [panel-master file]
#   sudo ./install.sh agent   [panel-agent file]
set -euo pipefail

install_master() {
  install -m 0755 "${1:-panel-master}" /usr/local/bin/panel-master
  id panel >/dev/null 2>&1 || useradd --system --home /var/lib/panel --shell /usr/sbin/nologin panel
  mkdir -p /var/lib/panel
  chown -R panel:panel /var/lib/panel
  install -m 0644 deploy/systemd/panel-master.service /etc/systemd/system/
  systemctl daemon-reload
  systemctl enable --now panel-master
  echo "master installed. Create the first admin:"
  echo "  sudo -u panel /usr/local/bin/panel-master -data /var/lib/panel -create-admin admin:yourpassword"
}

install_agent() {
  install -m 0755 "${1:-panel-agent}" /usr/local/bin/panel-agent
  id panel >/dev/null 2>&1 || useradd --system --home /var/lib/panel-agent --shell /usr/sbin/nologin panel
  mkdir -p /etc/panel /var/lib/panel-agent
  if [ ! -f /etc/panel/agent.json ]; then
    install -m 0600 configs/agent.example.json /etc/panel/agent.json
    echo "Edit /etc/panel/agent.json: set masterUrl and the node token from the panel UI."
  fi
  chown -R panel:panel /var/lib/panel-agent
  install -m 0644 deploy/systemd/panel-agent.service /etc/systemd/system/
  systemctl daemon-reload
  systemctl enable --now panel-agent
  echo "agent installed."
}

case "${1:-}" in
  master) install_master "${2:-}" ;;
  agent)  install_agent  "${2:-}" ;;
  *) echo "usage: $0 master|agent [binary]" >&2; exit 1 ;;
esac
