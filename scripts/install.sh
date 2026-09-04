#!/usr/bin/env bash
set -euo pipefail

REPO_URL="https://github.com/Xhiveee/panel"
BRANCH="main"
GO_VERSION="1.23.12"
# Custom sources only on explicit opt-in: env-driven URLs are a supply-chain
# RCE vector when the installer runs as root.
if [[ "${PANEL_ALLOW_CUSTOM_REPO:-0}" == "1" ]]; then
  REPO_URL="${PANEL_REPO_URL:-$REPO_URL}"
  BRANCH="${PANEL_BRANCH:-$BRANCH}"
  GO_VERSION="${PANEL_GO_VERSION:-$GO_VERSION}"
fi
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT
# Вся панель живёт в одном корне: бинарники, данные и конфиги не раскиданы
# по серверу. Исключение — systemd units (systemd требует /etc/systemd/system).
PANEL_ROOT="/opt/panel"
BIN_DIR="$PANEL_ROOT/bin"
MASTER_DATA="$PANEL_ROOT/master-data"
AGENT_DATA="$PANEL_ROOT/agent-data"
AGENT_JSON="$PANEL_ROOT/agent.json"

log() { printf '\n\033[1;36m%s\033[0m\n' "$*" >&2; }
die() { printf '\033[1;31mОшибка: %s\033[0m\n' "$*" >&2; exit 1; }
ask() {
  local prompt="$1" default="${2:-}" value
  read -r -p "$prompt" value </dev/tty || die "не удалось прочитать ввод"
  printf '%s' "${value:-$default}"
}
# ask_secret reads without echo (passwords/tokens must not be shoulder-surfed
# or stored in shell history).
ask_secret() {
  local prompt="$1" value
  read -r -s -p "$prompt" value </dev/tty || die "не удалось прочитать ввод"
  printf '\n' >&2
  printf '%s' "$value"
}
run_as_panel() {
  if command -v runuser >/dev/null 2>&1; then
    runuser -u panel -- "$@"
  elif command -v setpriv >/dev/null 2>&1; then
    setpriv --reuid panel --regid panel --clear-groups -- "$@"
  elif command -v /bin/bash >/dev/null 2>&1 && [[ -n "${BASH_VERSION:-}" ]]; then
    # %q is bash syntax: only use it with bash, never with /bin/sh (dash).
    su -s /bin/bash panel -c "$(printf '%q ' "$@")"
  else
    die "нужен runuser или setpriv для безопасного запуска от panel"
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
  printf '%s\n' \
    'Master — веб-панель, API и общая авторизация.' \
    'Agent  — локальное управление Minecraft-серверами на ноде.' \
    '' \
    'Что сделать?' \
    '  1) Установить Master' \
    '  2) Установить Agent' \
    '  3) Установить Master и Agent' \
    '  4) Удалить Master' \
    '  5) Удалить Agent' \
    '  6) Удалить Master и Agent'
  choice="$(ask 'Выберите 1-6: ')"
  case "$choice" in
    1) mode="master" ;;
    2) mode="agent" ;;
    3) mode="both" ;;
    4) mode="remove-master" ;;
    5) mode="remove-agent" ;;
    6) mode="remove-both" ;;
    *) die "выберите число от 1 до 6" ;;
  esac
fi
[[ "$mode" == "master" || "$mode" == "agent" || "$mode" == "both" ||
  "$mode" == "remove-master" || "$mode" == "remove-agent" || "$mode" == "remove-both" ]] ||
  die "неизвестный режим: $mode"

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
  curl -fsSL --proto '=https' --tlsv1.2 "https://go.dev/dl/$archive" -o "$WORK_DIR/$archive"
  curl -fsSL --proto '=https' --tlsv1.2 "https://go.dev/dl/$archive.sha256" -o "$WORK_DIR/$archive.sha256"
  (cd "$WORK_DIR" && sha256sum -c "$archive.sha256") || die "SHA256 Go не сошёлся, прерываю установку"
  mkdir -p "$WORK_DIR/go"
  tar -C "$WORK_DIR/go" --strip-components=1 -xzf "$WORK_DIR/$archive"
  export GOROOT="$WORK_DIR/go"
  export PATH="$GOROOT/bin:$PATH"
  export GOTOOLCHAIN=local
  "$GOROOT/bin/go" version
}

download_source() {
  log "Скачиваю Panel"
  curl -fsSL --proto '=https' --tlsv1.2 "$REPO_URL/archive/refs/heads/$BRANCH.tar.gz" -o "$WORK_DIR/panel.tar.gz"
  tar -xzf "$WORK_DIR/panel.tar.gz" -C "$WORK_DIR"
  SOURCE_DIR="$(find "$WORK_DIR" -mindepth 1 -maxdepth 1 -type d -name 'panel-*' | head -n 1)"
  [[ -n "${SOURCE_DIR:-}" ]] || die "не удалось распаковать исходники"
}

build_binary() {
  local component="$1" output="$WORK_DIR/panel-$1"
  log "Собираю $component"
  (
    cd "$SOURCE_DIR"
    if [[ "$component" == "master" ]]; then
      "$GOROOT/bin/go" build -o "$output" ./master/cmd/master
    else
      "$GOROOT/bin/go" build -o "$output" ./agent/cmd/agent
    fi
  )
  [[ -x "$output" ]] || die "сборка не создала бинарник"
  printf '%s' "$output"
}

remove_master() {
  systemctl disable --now panel-master 2>/dev/null || true
  rm -f /etc/systemd/system/panel-master.service "$BIN_DIR/panel-master"
  rm -rf "$MASTER_DATA"
  systemctl daemon-reload
  rmdir "$BIN_DIR" 2>/dev/null || true
  rmdir "$PANEL_ROOT" 2>/dev/null || true
  log "Master полностью удалён: сервис, бинарник и данные ($MASTER_DATA)"
}

remove_agent() {
  systemctl disable --now panel-agent 2>/dev/null || true
  rm -f /etc/systemd/system/panel-agent.service "$BIN_DIR/panel-agent" "$AGENT_JSON"
  rm -rf "$AGENT_DATA"
  systemctl daemon-reload
  rmdir "$BIN_DIR" 2>/dev/null || true
  rmdir "$PANEL_ROOT" 2>/dev/null || true
  log "Agent полностью удалён: сервис, бинарник, конфиг и данные ($AGENT_DATA)"
}

show_master_info() {
  local username="$1" ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [[ -n "$ip" ]] || ip="<IP_СЕРВЕРА>"
  printf '\n'
  log "Готово: Master запущен"
  printf 'Откройте панель: http://%s:8080\n' "$ip"
  printf 'Локальный адрес:  http://127.0.0.1:8080\n'
  printf 'Логин:            %s\n' "$username"
  printf 'Пароль:           тот, который вы ввели выше\n'
  printf '\n'
  printf 'Корень панели:    %s\n' "$PANEL_ROOT"
  printf 'Данные Master:     %s\n' "$MASTER_DATA"
  printf 'Статус:            systemctl status panel-master --no-pager\n'
  printf 'Логи:              journalctl -u panel-master -n 50 --no-pager\n'
  printf '\n'
  printf 'Следующий шаг: войдите в панель, откройте раздел «Ноды», создайте ноду\n'
  printf 'и установите Agent на сервере, где находятся Minecraft-серверы.\n'
}

show_agent_info() {
  printf '\n'
  log "Готово: Agent установлен и запущен"
  printf 'Корень панели:      %s\n' "$PANEL_ROOT"
  printf 'Конфигурация:      %s\n' "$AGENT_JSON"
  printf 'Данные Agent:      %s\n' "$AGENT_DATA"
  printf 'Статус:            systemctl status panel-agent --no-pager\n'
  printf 'Логи:              journalctl -u panel-agent -n 50 --no-pager\n'
  printf '\n'
  printf 'Если Agent не подключается, проверьте Master URL, токен и доступность\n'
  printf 'порта 8080 (или reverse proxy URL wss://... для HTTPS).\n'
}

install_master() {
  local username admin_pass pid i log_file ready cred_file
  username="$(ask 'Логин администратора [admin]: ' admin)"
  admin_pass="$(ask_secret 'Пароль администратора (минимум 8 символов): ')"
  [[ ${#admin_pass} -ge 8 ]] || die "пароль должен содержать минимум 8 символов"
  valid_value "$username" || die "логин содержит недопустимые символы"
  valid_value "$admin_pass" || die "пароль содержит недопустимые символы"

  install -m 0755 -D "$BINARY" "$BIN_DIR/panel-master"
  id panel >/dev/null 2>&1 || useradd --system --home "$PANEL_ROOT" --shell /usr/sbin/nologin panel
  mkdir -p "$MASTER_DATA"
  # One-time migration from the pre-/opt/panel layout.
  if [[ ! -f "$MASTER_DATA/panel.db" && -f /var/lib/panel/panel.db ]]; then
    log "Переношу базу Master из /var/lib/panel в $MASTER_DATA"
    mv /var/lib/panel/panel.db* "$MASTER_DATA/" 2>/dev/null || true
    rmdir /var/lib/panel 2>/dev/null || true
  fi
  chown -R panel:panel "$PANEL_ROOT"
  install -m 0644 "$SOURCE_DIR/deploy/systemd/panel-master.service" /etc/systemd/system/panel-master.service

  systemctl stop panel-master 2>/dev/null || true
  log "Создаю администратора"
  log_file="$WORK_DIR/bootstrap.log"
  # Credentials via a 0600 file: never pass the password in argv (ps/journal).
  cred_file="$WORK_DIR/admin.cred"
  printf '%s:%s' "$username" "$admin_pass" >"$cred_file"
  chmod 600 "$cred_file"
  admin_pass=""
  run_as_panel "$BIN_DIR/panel-master" -data "$MASTER_DATA" \
    -create-admin-file "$cred_file" >"$log_file" 2>&1 &
  pid=$!
  ready=0
  for ((i=0; i<30; i++)); do
    if grep -q "master listening" "$log_file" 2>/dev/null; then
      ready=1
      break
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      # Bootstrap exited before listening: show the real error.
      wait "$pid" 2>/dev/null || true
      cat "$log_file" >&2
      die "не удалось создать администратора (см. лог выше)"
    fi
    sleep 1
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -f "$cred_file"
  if [[ "$ready" -ne 1 ]]; then
    cat "$log_file" >&2
    die "Master не запустился за отведённое время"
  fi

  systemctl daemon-reload
  systemctl enable --now panel-master
  show_master_info "$username"
}

install_agent() {
  local master_url token name
  master_url="$(ask 'URL WebSocket master [wss://MASTER:8080/api/agent/ws]: ')"
  token="$(ask_secret 'Токен ноды из панели: ')"
  printf '\n' >&2
  name="$(ask 'Имя ноды [node-1]: ' node-1)"
  [[ -n "$master_url" && -n "$token" && -n "$name" ]] || die "все значения обязательны"
  valid_value "$master_url" && valid_value "$name" ||
    die "значения содержат недопустимые символы"
  [[ "$master_url" == ws://* || "$master_url" == wss://* ]] || die "master URL должен начинаться с ws:// или wss:// (для продакшна — wss://)"
  [[ "$master_url" == wss://* ]] || log "Внимание: ws:// без TLS — токен и консоль видны в сети, используйте wss://"

  install -m 0755 -D "$BINARY" "$BIN_DIR/panel-agent"
  id panel >/dev/null 2>&1 || useradd --system --home "$PANEL_ROOT" --shell /usr/sbin/nologin panel
  mkdir -p "$PANEL_ROOT" "$AGENT_DATA"
  # One-time migration from the pre-/opt/panel layout (config keeps working:
  # absolute dataDir inside stays valid).
  if [[ ! -f "$AGENT_JSON" && -f /etc/panel/agent.json ]]; then
    log "Переношу конфиг Agent из /etc/panel/agent.json в $AGENT_JSON"
    cp /etc/panel/agent.json "$AGENT_JSON"
    rmdir /etc/panel 2>/dev/null || true
  fi
  chown -R panel:panel "$PANEL_ROOT"
  # JSON without printf-injection: escape via python3 (preferred) or jq.
  if command -v python3 >/dev/null 2>&1; then
    MASTER_URL="$master_url" TOKEN="$token" DATA_DIR="$AGENT_DATA" NAME="$name" AGENT_JSON="$AGENT_JSON" python3 -c \
      'import json,os; json.dump({"masterUrl":os.environ["MASTER_URL"],"token":os.environ["TOKEN"],"dataDir":os.environ["DATA_DIR"],"name":os.environ["NAME"]}, open(os.environ["AGENT_JSON"],"w"), indent=2)' \
      || die "не удалось записать agent.json"
  elif command -v jq >/dev/null 2>&1; then
    jq -n --arg m "$master_url" --arg t "$token" --arg d "$AGENT_DATA" --arg n "$name" \
      '{masterUrl:$m,token:$t,dataDir:$d,name:$n}' > "$AGENT_JSON" \
      || die "не удалось записать agent.json"
  else
    die "нужен python3 или jq для безопасной записи agent.json"
  fi
  token=""; master_url=""
  chown panel:panel "$AGENT_JSON"
  chmod 600 "$AGENT_JSON"
  install -m 0644 "$SOURCE_DIR/deploy/systemd/panel-agent.service" /etc/systemd/system/panel-agent.service
  systemctl daemon-reload
  systemctl enable --now panel-agent
  show_agent_info
}

if [[ "$mode" == "remove-master" ]]; then
  remove_master
elif [[ "$mode" == "remove-agent" ]]; then
  remove_agent
elif [[ "$mode" == "remove-both" ]]; then
  remove_master
  remove_agent
  # Оба компонента снесены вместе с данными — удаляем корень целиком и
  # системного пользователя, чтобы не оставалось следов.
  rm -rf "$PANEL_ROOT"
  userdel panel 2>/dev/null || true
  log "Корень $PANEL_ROOT и пользователь panel удалены"
else
  install_go
  download_source
  if [[ "$mode" == "master" || "$mode" == "both" ]]; then
    BINARY="$(build_binary master)"
    install_master
  fi
  if [[ "$mode" == "agent" || "$mode" == "both" ]]; then
    BINARY="$(build_binary agent)"
    install_agent
  fi
fi
