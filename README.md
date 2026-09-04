# Panel — управление Minecraft-серверами

Минималистичная веб-панель: **master** (веб-UI + API, SQLite) и **agent** (тонкий клиент на каждой ноде: процессы, консоль, файлы, метрики).

## Установка на Linux-сервер (VPS/VDS/Dedicated)

Панель состоит из **master** и одного или нескольких **agent**:

- master запускается на одной управляющей машине и предоставляет веб-интерфейс;
- agent устанавливается на каждой машине, где находятся Minecraft-инстансы.

Master не должен напрямую управлять Docker/systemd на удалённых машинах. Agent
подключается к master по WebSocket и продолжает работать независимо от браузера.

### Требования

- Debian/Ubuntu или другой Linux с systemd;
- Go 1.27+ только для сборки из исходников (CGO не используется);
- root-доступ через `sudo`;
- открытый TCP-порт master (по умолчанию `8080`) только для пользователей панели;
- на машинах agent нужен исходный каталог Minecraft и Java/другие программы,
  указанные в команде запуска инстанса.

### 1. Скачать исходники и собрать бинарники

На машине с установленным Go:

```bash
sudo apt update
sudo apt install -y git golang
git clone https://github.com/Xhiveee/panel panel
cd panel
go build -o panel-master ./master/cmd/master
go build -o panel-agent ./agent/cmd/agent
```

Если Go уже установлен, достаточно выполнить две команды `go build`. Полученные
бинарники можно скопировать на серверы через `scp`:

```bash
scp panel-master user@MASTER_IP:/tmp/
scp panel-agent user@AGENT_IP:/tmp/
```

Файлы в `bin/` с расширением `.exe` — это Windows-сборки для локальной
разработки. Linux использует нативные ELF-бинарники без расширения `.exe`:
`panel-master` и `panel-agent`. Собирать их можно прямо на Linux или кросс-
компиляцией:

```bash
GOOS=linux GOARCH=amd64 go build -o panel-master ./master/cmd/master
GOOS=linux GOARCH=amd64 go build -o panel-agent ./agent/cmd/agent
```

### 2. Установить и запустить master

На master-сервере из корня распакованного репозитория (на сервер нужно
скопировать также каталог `scripts/` и unit-файлы из `deploy/`):

```bash
sudo ./scripts/install.sh master /tmp/panel-master
```

Скрипт устанавливает бинарник в `/usr/local/bin/panel-master`, создаёт системного
пользователя `panel`, каталог данных `/var/lib/panel` и unit systemd.

При первом запуске master автоматически создаёт администратора и печатает
одноразовые реквизиты в журнал systemd. Получите их командой:

```bash
sudo journalctl -u panel-master -n 50 --no-pager
sudo systemctl status panel-master --no-pager
```

Найдите блок `initial admin account`, войдите с указанными реквизитами и сразу
смените пароль в разделе **Настройки**. Если аккаунт уже существует, повторный
запуск с `-create-admin` не нужен.

Откройте `http://MASTER_IP:8080`. Для публичного сервера рекомендуется поставить
Nginx/Caddy перед master и использовать HTTPS. В этом случае agent должен
подключаться по `wss://`, а не по `ws://`.

### 3. Открыть сетевой доступ

Откройте порт master только в нужных источниках. Например, для UFW:

```bash
sudo ufw allow from YOUR_IP to any port 8080 proto tcp
sudo ufw enable
```

Agent не требует входящего порта: ему нужен исходящий доступ к
`MASTER_IP:8080` (или к HTTPS-порту reverse proxy).

### 4. Добавить agent-ноду в панели

1. Войдите администратором.
2. Откройте **Ноды → Добавить ноду**.
3. Скопируйте показанный токен — он отображается один раз.
4. На сервере agent создайте конфигурацию `/etc/panel/agent.json` по примеру
   [`configs/agent.example.json`](configs/agent.example.json):

```json
{
  "masterUrl": "ws://MASTER_IP:8080/api/agent/ws",
  "token": "ТОКЕН_НОДЫ",
  "dataDir": "/var/lib/panel-agent",
  "name": "node-1"
}
```

Для HTTPS reverse proxy используйте, например:
`"masterUrl": "wss://panel.example.com/api/agent/ws"`.

### 5. Установить и запустить agent

На каждой agent-машине из корня репозитория (или после клонирования репозитория
на эту машину):

```bash
sudo ./scripts/install.sh agent /tmp/panel-agent
sudo nano /etc/panel/agent.json
sudo chown panel:panel /etc/panel/agent.json
sudo chmod 600 /etc/panel/agent.json
sudo systemctl restart panel-agent
sudo systemctl status panel-agent --no-pager
```

Проверить подключение можно так:

```bash
sudo journalctl -u panel-agent -f
```

После подключения нода должна иметь статус `online` в разделе **Ноды**.

### 6. Создать Minecraft-инстанс

В панели откройте **Инстансы → Создать** и укажите:

- имя;
- ноду;
- абсолютный путь к каталогу сервера, например `/srv/minecraft/survival`;
- команду запуска, например `java -Xmx2G -jar server.jar nogui`;
- stop-команду `stop`.

Убедитесь, что пользователь `panel` имеет права на каталог инстанса:

```bash
sudo chown -R panel:panel /srv/minecraft/survival
```

После этого доступны запуск/остановка, консоль, файлы и метрики.

### Обновление и диагностика

Данные master хранятся в `/var/lib/panel`; конфигурация agent — в
`/etc/panel/agent.json`. Обновление бинарника не удаляет SQLite и конфигурацию:

```bash
sudo install -m 0755 ./panel-master /usr/local/bin/panel-master
sudo systemctl restart panel-master
sudo journalctl -u panel-master -n 100 --no-pager
```

Перед обновлением рекомендуется сделать резервную копию `/var/lib/panel`.

## Сборка

Нужен только Go (1.27+). CGO не используется.

```bash
go build -o panel-master ./master/cmd/master
go build -o panel-agent ./agent/cmd/agent
```

## Локальный запуск (Windows/Linux)

1. Master:
   ```
   .\panel-master.exe -create-admin admin:admin123
   ```
   Открой http://localhost:8080 и войди.

2. Нода (admin → Ноды → «Добавить ноду», скопируй токен), затем:
   ```
   .\panel-agent.exe -master ws://localhost:8080/api/agent/ws -token <TOKEN> -data .\agent-data
   ```

3. Создай инстанс (например команда `cmd /c ping -t 127.0.0.1`), запусти его и пользуйся консолью/файлами/метриками.

## Структура

- `master/` — веб-сервер: API, WebSocket-консоль, пользователи/RBAC, ноды, инстансы, аудит, embedded UI.
- `agent/` — локальный раннер процессов, файловый менеджер (safe path), метрики (gopsutil), WebSocket-клиент к master.
- `common/protocol` — типизированный протокол master ↔ agent.
- `deploy/`, `scripts/`, `configs/` — юниты systemd, install-скрипт, пример конфига агента.
