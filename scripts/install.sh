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
  local delete_data
  systemctl disable --now panel-master 2>/dev/null || true
  rm -f /etc/systemd/system/panel-master.service /usr/local/bin/panel-master
  systemctl daemon-reload
  delete_data="$(ask 'Удалить данные Master (/var/lib/panel)? [y/N]: ' n)"
  if [[ "$delete_data" =~ ^[YyДд]$ ]]; then
    rm -rf /var/lib/panel
    log "Master и его данные удалены"
  else
    log "Master удалён, данные сохранены в /var/lib/panel"
  fi
}

remove_agent() {
  local delete_data
  systemctl disable --now panel-agent 2>/dev/null || true
  rm -f /etc/systemd/system/panel-agent.service /usr/local/bin/panel-agent /etc/panel/agent.json
  systemctl daemon-reload
  delete_data="$(ask 'Удалить данные Agent (/var/lib/panel-agent)? [y/N]: ' n)"
  if [[ "$delete_data" =~ ^[YyДд]$ ]]; then
    rm -rf /var/lib/panel-agent
    log "Agent и его данные удалены"
  else
    log "Agent удалён, данные сохранены в /var/lib/panel-agent"
  fi
}

show_master_info() {
  local username="$1" existing="$2" ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [[ -n "$ip" ]] || ip="<IP_СЕРВЕРА>"
  printf '\n'
  log "Готово: Master запущен"
  printf 'Откройте панель: http://%s:8080\n' "$ip"
  printf 'Локальный адрес:  http://127.0.0.1:8080\n'
  printf 'Логин:            %s\n' "$username"
  if [[ "$existing" -eq 1 ]]; then
    printf 'Пароль:            используется прежний пароль существующего пользователя\n'
  else
    printf 'Пароль:            тот, который вы ввели выше\n'
  fi
  printf '\n'
  printf 'Данные Master:     /var/lib/panel\n'
  printf 'Статус:            systemctl status panel-master --no-pager\n'
  printf 'Логи:              journalctl -u panel-master -n 50 --no-pager\n'
  printf '\n'
  printf 'Следующий шаг: войдите в панель, откройте раздел «Ноды», создайте ноду\n'
  printf 'и установите Agent на сервере, где находятся Minecraft-серверы.\n'
}

show_agent_info() {
  local data_dir="$1"
  printf '\n'
  log "Готово: Agent установлен и запущен"
  printf 'Конфигурация:      /etc/panel/agent.json\n'
  printf 'Данные Agent:      %s\n' "$data_dir"
  printf 'Статус:            systemctl status panel-agent --no-pager\n'
  printf 'Логи:              journalctl -u panel-agent -n 50 --no-pager\n'
  printf '\n'
  printf 'Если Agent не подключается, проверьте Master URL, токен и доступность\n'
  printf 'порта 8080 (или reverse proxy URL wss://... для HTTPS).\n'
}

install_master() {
  local username admin_pass pid i log_file ready existing
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
  existing=0
  for ((i=0; i<30; i++)); do
    if grep -q "master listening" "$log_file" 2>/dev/null; then
      ready=1
      break
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      if wait "$pid"; then
        :
      elif grep -q "UNIQUE constraint failed: users.username" "$log_file" 2>/dev/null; then
        existing=1
        break
      else
        cat "$log_file" >&2
        die "не удалось создать администратора"
      fi
    fi
    sleep 1
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  cat "$log_file"
  if [[ "$existing" -eq 1 ]]; then
    log "Пользователь уже существует, сохраняю текущую учётную запись"
  elif [[ "$ready" -ne 1 ]]; then
    die "Master не запустился за отведённое время"
  fi

  systemctl daemon-reload
  systemctl enable --now panel-master
  show_master_info "$username" "$existing"
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
  show_agent_info "$data_dir"
}

if [[ "$mode" == "remove-master" ]]; then
  remove_master
elif [[ "$mode" == "remove-agent" ]]; then
  remove_agent
elif [[ "$mode" == "remove-both" ]]; then
  remove_master
  remove_agent
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
