# Panel

Веб-панель для управления Minecraft-серверами. Master запускается на одной
машине, Agent — на каждой машине с Minecraft-инстансами.

## Установка на Linux

Запустите на сервере master или agent:

Под `root` выполните:

```bash
curl -fsSL https://github.com/Xhiveee/panel/raw/b39b3c6/scripts/install.sh | bash
```

Если вы вошли обычным пользователем и в системе установлен `sudo`:

```bash
curl -fsSL https://raw.githubusercontent.com/Xhiveee/panel/main/scripts/install.sh | sudo bash
```

Скрипт интерактивно:

- установит Go, если он отсутствует;
- скачает исходники Panel и соберёт Linux-бинарники;
- установит и запустит systemd-сервис;
- настроит администратора для Master или токен/URL для Agent.

Сначала установите **Master**, войдите в панель по адресу `http://SERVER_IP:8080`
и создайте ноду в разделе **Ноды**. Затем запустите эту же команду на каждой
машине Agent и вставьте полученный токен.

Для публичного сервера рекомендуется использовать HTTPS reverse proxy. В этом
случае укажите Agent URL вида `wss://panel.example.com/api/agent/ws`.

Данные Master хранятся в `/var/lib/panel`, конфигурация Agent — в
`/etc/panel/agent.json`. Управление сервисами:

```bash
sudo systemctl status panel-master
sudo systemctl status panel-agent
sudo journalctl -u panel-master -f
sudo journalctl -u panel-agent -f
```

## Сборка вручную

Требуется Go 1.23+:

```bash
go build -o panel-master ./master/cmd/master
go build -o panel-agent ./agent/cmd/agent
```

## Локальный запуск

```bash
./panel-master -addr :8080 -data ./panel-data -create-admin admin:your-password
./panel-agent -master ws://localhost:8080/api/agent/ws -token NODE_TOKEN -data ./agent-data
```

## Структура

- `master/` — веб-интерфейс, API, авторизация, ноды, инстансы и аудит;
- `agent/` — процессы, консоль, файлы и метрики на ноде;
- `common/protocol/` — протокол Master ↔ Agent;
- `deploy/` и `scripts/` — systemd units и установщик.
