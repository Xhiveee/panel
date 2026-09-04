#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${PANEL_REPO_URL:-https://github.com/Xhiveee/panel}"
BRANCH="${PANEL_BRANCH:-main}"
GO_VERSION="${PANEL_GO_VERSION:-1.23.12}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

log() { printf '\n\033[1;36m%s\033[0m\n' "$*" >&2; }
die() { printf '\033[1;31mОшибка: %s\033[0m\n' "$*" >&2; exit 1; }
ask() {
  local prompt="$1" default="${2:-}" value
  read -r -p "$prompt" value </dev/tty || die "не удалось прочитать ввод"
  printf '%s' "${value:-$default}"
}
run_as_panel() {
  if command -v runuser >/dev/null 2>&1; then
    runuser -u panel -- "$@"
  else
    su -s /bin/sh panel -c "$(printf '%q ' "$@")"
  fi
}
valid_value() {
  [[ "$1" != *$'\n'* && "$1" != *$'\r'* && "$1" != *'"'* && "$1" != *'\'* ]]
}

[[ "$(id -u)" -eq 0 ]] || die "запустите скрипт от root"
command -v curl >/dev/null 2>&1 || die "не найден curl"
command -v tar >/dev/null 2>&1 || die "не найден tar"
command -v systemctl >/dev/null 2>&1 || die "не найден systemctl"

mode="${1:-}"
if [[ -z "$mode" ]]; then
  printf 'Что установить?\n  1) Master\n  2) Agent\n'
  choice="$(ask 'Выберите 1 или 2: ')"
  case "$choice" in
    1) mode="master" ;;
    2) mode="agent" ;;
    *) die "выберите 1 или 2" ;;
  esac
fi
[[ "$mode" == "master" || "$mode" == "agent" ]] || die "выберите master или agent"

install_go() {
  local arch archive
  case "$(uname -m)" in
    x86_64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    armv6l|armv7l) arch="armv6l" ;;
    *) die "неподдерживаемая архитектура: $(uname -m)" ;;
  esac
  archive="go${GO_VERSION}.linux-${arch}.tar.gz"
  log "Скачиваю Go $GO_VERSION в локальный каталог"
  curl -fsSL "https://go.dev/dl/$archive" -o "$WORK_DIR/$archive"
  mkdir -p "$WORK_DIR/go"
  tar -C "$WORK_DIR/go" --strip-components=1 -xzf "$WORK_DIR/$archive"
  export GOROOT="$WORK_DIR/go"
  export PATH="$GOROOT/bin:$PATH"
  export GOTOOLCHAIN=local
  "$GOROOT/bin/go" version
}

download_source() {
  log "Скачиваю Panel"
  curl -fsSL "$REPO_URL/archive/refs/heads/$BRANCH.tar.gz" -o "$WORK_DIR/panel.tar.gz"
  tar -xzf "$WORK_DIR/panel.tar.gz" -C "$WORK_DIR"
  SOURCE_DIR="$(find "$WORK_DIR" -mindepth 1 -maxdepth 1 -type d -name 'panel-*' | head -n 1)"
  [[ -n "${SOURCE_DIR:-}" ]] || die "не удалось распаковать исходники"
}

build_binary() {
  local output="$WORK_DIR/panel-$mode"
  log "Собираю $mode"
  (
    cd "$SOURCE_DIR"
    if [[ "$mode" == "master" ]]; then
      "$GOROOT/bin/go" build -o "$output" ./master/cmd/master
    else
      "$GOROOT/bin/go" build -o "$output" ./agent/cmd/agent
    fi
  )
  [[ -x "$output" ]] || die "сборка не создала бинарник"
  printf '%s' "$output"
}

install_master() {
  local username admin_pass pid i log_file ready
  username="$(ask 'Логин администратора [admin]: ' admin)"
  admin_pass="$(ask 'Пароль администратора (минимум 6 символов): ')"
  [[ ${#admin_pass} -ge 6 ]] || die "пароль должен содержать минимум 6 символов"
  valid_value "$username" || die "логин содержит недопустимые символы"
  valid_value "$admin_pass" || die "пароль содержит недопустимые символы"

  install -m 0755 "$BINARY" /usr/local/bin/panel-master
  id panel >/dev/null 2>&1 || useradd --system --home /var/lib/panel --shell /usr/sbin/nologin panel
  mkdir -p /var/lib/panel
  chown -R panel:panel /var/lib/panel
  install -m 0644 "$SOURCE_DIR/deploy/systemd/panel-master.service" /etc/systemd/system/panel-master.service

  systemctl stop panel-master 2>/dev/null || true
  log "Создаю администратора"
  log_file="$WORK_DIR/bootstrap.log"
  run_as_panel /usr/local/bin/panel-master -data /var/lib/panel \
    -create-admin "$username:$admin_pass" >"$log_file" 2>&1 &
  pid=$!
  ready=0
  for ((i=0; i<30; i++)); do
    if grep -q "master listening" "$log_file" 2>/dev/null; then
      ready=1
      break
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" || { cat "$log_file" >&2; die "не удалось создать администратора"; }
    fi
    sleep 1
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  cat "$log_file"
  [[ "$ready" -eq 1 ]] || die "Master не запустился за отведённое время"

  systemctl daemon-reload
  systemctl enable --now panel-master
  log "Master установлен и запущен"
}

install_agent() {
  local master_url token data_dir name
  master_url="$(ask 'URL WebSocket master [ws://MASTER_IP:8080/api/agent/ws]: ')"
  token="$(ask 'Токен ноды из панели: ')"
  data_dir="$(ask 'Каталог данных [/var/lib/panel-agent]: ' /var/lib/panel-agent)"
  name="$(ask 'Имя ноды [node-1]: ' node-1)"
  [[ -n "$master_url" && -n "$token" && -n "$data_dir" && -n "$name" ]] || die "все значения обязательны"
  valid_value "$master_url" && valid_value "$token" && valid_value "$data_dir" && valid_value "$name" ||
    die "значения содержат недопустимые символы"

  install -m 0755 "$BINARY" /usr/local/bin/panel-agent
  id panel >/dev/null 2>&1 || useradd --system --home /var/lib/panel-agent --shell /usr/sbin/nologin panel
  mkdir -p /etc/panel "$data_dir"
  chown -R panel:panel "$data_dir"
  printf '{\n  "masterUrl": "%s",\n  "token": "%s",\n  "dataDir": "%s",\n  "name": "%s"\n}\n' \
    "$master_url" "$token" "$data_dir" "$name" > /etc/panel/agent.json
  chown panel:panel /etc/panel/agent.json
  chmod 600 /etc/panel/agent.json
  install -m 0644 "$SOURCE_DIR/deploy/systemd/panel-agent.service" /etc/systemd/system/panel-agent.service
  systemctl daemon-reload
  systemctl enable --now panel-agent
  log "Agent установлен и запущен"
}

install_go
download_source
BINARY="$(build_binary)"
if [[ "$mode" == "master" ]]; then
  install_master
else
  install_agent
fi
