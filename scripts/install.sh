#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${PANEL_REPO_URL:-https://github.com/Xhiveee/panel}"
BRANCH="${PANEL_BRANCH:-main}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

log() { printf '\n\033[1;36m%s\033[0m\n' "$*"; }
die() { printf '\033[1;31mОшибка: %s\033[0m\n' "$*" >&2; exit 1; }
ask() { local prompt="$1" default="${2:-}" value; read -r -p "$prompt" value; printf '%s' "${value:-$default}"; }
ask_secret() { local prompt="$1" value; read -r -s -p "$prompt" value; printf '\n' >&2; printf '%s' "$value"; }

[[ "$(id -u)" -eq 0 ]] || die "запустите скрипт через sudo"
command -v curl >/dev/null || die "не найден curl"
command -v tar >/dev/null || die "не найден tar"

install_go() {
  if command -v go >/dev/null; then
    local version major minor
    version="$(go version | awk '{print $3}' | sed 's/^go//')"
    major="${version%%.*}"
    minor="${version#*.}"
    minor="${minor%%.*}"
    if [[ "$major" =~ ^[0-9]+$ && "$minor" =~ ^[0-9]+$ ]] &&
      { (( major > 1 )) || (( major == 1 && minor >= 23 )); }; then
      return
    fi
    log "Найден старый Go $version, устанавливаю Go 1.23+"
  fi
  log "Устанавливаю Go"
  if command -v apt-get >/dev/null; then
    apt-get update
    apt-get install -y golang-go
  elif command -v dnf >/dev/null; then
    dnf install -y golang
  elif command -v yum >/dev/null; then
    yum install -y golang
  else
    die "Go не найден, а пакетный менеджер не поддерживается"
  fi
}

download_source() {
  log "Скачиваю Panel"
  curl -fsSL "$REPO_URL/archive/refs/heads/$BRANCH.tar.gz" -o "$WORK_DIR/panel.tar.gz"
  tar -xzf "$WORK_DIR/panel.tar.gz" -C "$WORK_DIR"
  SOURCE_DIR="$(find "$WORK_DIR" -mindepth 1 -maxdepth 1 -type d -name 'panel-*' | head -n 1)"
  [[ -n "${SOURCE_DIR:-}" ]] || die "не удалось распаковать исходники"
}

install_master() {
  local username password
  username="$(ask 'Логин администратора [admin]: ' admin)"
  password="$(ask_secret 'Пароль администратора (минимум 6 символов): ')"
  [[ "${#password}" -ge 6 ]] || die "пароль должен содержать минимум 6 символов"

  install -m 0755 "$SOURCE_DIR/panel-master" /usr/local/bin/panel-master
  id panel >/dev/null 2>&1 || useradd --system --home /var/lib/panel --shell /usr/sbin/nologin panel
  mkdir -p /var/lib/panel
  chown -R panel:panel /var/lib/panel
  install -m 0644 "$SOURCE_DIR/deploy/systemd/panel-master.service" /etc/systemd/system/

  systemctl stop panel-master 2>/dev/null || true
  sudo -u panel /usr/local/bin/panel-master -data /var/lib/panel \
    -create-admin "$username:$password"
  systemctl daemon-reload
  systemctl enable --now panel-master
  log "Master установлен: http://$(hostname -I | awk '{print $1}'):8080"
}

install_agent() {
  local master_url token data_dir name
  master_url="$(ask 'URL WebSocket master [ws://MASTER_IP:8080/api/agent/ws]: ')"
  token="$(ask_secret 'Токен ноды из панели: ')"
  data_dir="$(ask 'Каталог данных [/var/lib/panel-agent]: ' /var/lib/panel-agent)"
  name="$(ask 'Имя ноды [node-1]: ' node-1)"

  install -m 0755 "$SOURCE_DIR/panel-agent" /usr/local/bin/panel-agent
  id panel >/dev/null 2>&1 || useradd --system --home /var/lib/panel-agent --shell /usr/sbin/nologin panel
  mkdir -p /etc/panel "$data_dir"
  chown -R panel:panel "$data_dir"
  cat > /etc/panel/agent.json <<EOF
{
  "masterUrl": "$master_url",
  "token": "$token",
  "dataDir": "$data_dir",
  "name": "$name"
}
EOF
  chown panel:panel /etc/panel/agent.json
  chmod 600 /etc/panel/agent.json
  install -m 0644 "$SOURCE_DIR/deploy/systemd/panel-agent.service" /etc/systemd/system/
  systemctl daemon-reload
  systemctl enable --now panel-agent
  log "Agent установлен и запущен"
}

mode="${1:-}"
if [[ -z "$mode" ]]; then
  printf 'Что установить?\n  1) Master\n  2) Agent\n'
  choice="$(ask 'Выберите 1 или 2: ')"
  [[ "$choice" == "1" ]] && mode="master"
  [[ "$choice" == "2" ]] && mode="agent"
fi
[[ "$mode" == "master" || "$mode" == "agent" ]] || die "выберите master или agent"

install_go
download_source
(
  cd "$SOURCE_DIR"
  go build -o "$WORK_DIR/panel-master" ./master/cmd/master
  go build -o "$WORK_DIR/panel-agent" ./agent/cmd/agent
)
cp "$WORK_DIR/panel-master" "$SOURCE_DIR/panel-master"
cp "$WORK_DIR/panel-agent" "$SOURCE_DIR/panel-agent"

if [[ "$mode" == "master" ]]; then
  install_master
else
  install_agent
fi
