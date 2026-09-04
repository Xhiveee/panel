# План исправления: полный аудит безопасности и код-ревью Panel

Дата аудита: 2026-09-04. Источник: ручной разбор всего кода + `go vet` (чисто), `go test` (только `auth` с тестами), `go build` (master/agent OK).

Как пользоваться: идти по фазам P0 → P4. Каждый пункт — `[ ]` задача. После каждой фазы: `gofmt`, `go vet ./...`, `go test ./...`, сборка обоих бинарников, ручная проверка связанных страниц/API.

---

## P0 — Critical, эксплуатируется сейчас (делать первым)

### 1. IDOR в `listUserInstances`
- [ ] Файлы: `master/internal/api/api.go:69`, `master/internal/api/users_handlers.go:186-201`
- [ ] Проблема: `GET /api/users/{id}/instances` доступен любому авторизованному, без `RequireAdmin` и без проверки `id == self`. Перебор `id` отдает чужие привязки.
- [ ] Исправление: добавить проверку как в `changePassword` — `if !admin && id != self → 403`. Или повесить `RequireAdmin` + исключение self.
- [ ] Проверка: user A не может `GET /api/users/<B>/instances` (403), self — 200, admin — 200. Тест: два юзера, перекрестные запросы.

### 2. Угон консоли через любой сайт (CSWH)
- [ ] Файл: `master/internal/ws/console.go:69`
- [ ] Проблема: `OriginPatterns: ["*"]` + cookie-auth без CSRF. Чужой сайт открывает WS от имени жертвы, читает вывод и пишет в stdin.
- [ ] Исправление:
  - [ ] Убрать `"*"`, проверять `Origin` по конфигу (allowlist из `Addr`/настроек).
  - [ ] Добавить CSRF-токен для WS handshake (или требовать `Authorization: Bearer` вместо куки для WS).
  - [ ] Кука: `SameSite=Strict`, `Secure`, `__Host-` префикс (см. `auth_handlers.go:12-21`).
- [ ] Проверка: кросс-доменный WS отклоняется, same-origin работает. Тест с двумя Origin.

### 3. Нет TLS, секреты открытым текстом
- [ ] Файлы: `master/cmd/master/main.go:87-91`, `agent/internal/config/config.go`, `configs/agent.example.json`, `scripts/install.sh`
- [ ] Проблема: только `ReadHeaderTimeout`, нет `Read/Write/IdleTimeout`, нет HSTS/CSP/X-Frame-Options. Пример `ws://`, install печатает `http://IP:8080`.
- [ ] Исправление:
  - [ ] Добавить `ReadTimeout/WriteTimeout/IdleTimeout` в `http.Server` (защита от Slowloris).
  - [ ] Добавить security-headers middleware (HSTS, CSP, X-Frame-Options, X-Content-Type-Options).
  - [ ] Перевести пример и доку на `wss://`, описать обязательный reverse-proxy для публичного сервера.
  - [ ] Агент: enforce `ws/wss` схемы, опция проверки сертификата / mTLS (см. P1-1).
- [ ] Проверка: заголовки в ответе, `ws://` пример убран, дока обновлена.

### 4. Произвольный `Cmd/Dir/Env` в агенте (RCE by design)
- [ ] Файлы: `agent/internal/runner/runner.go:63-73`, `common/protocol/protocol.go:99-121`, `agent/internal/client/client.go:260-308`
- [ ] Проблема: `MkdirAll(Dir)` + `exec.Command(Cmd...)` + `Env=os.Environ()+spec.Env` без ограничений. `Dir` приезжает из сети в каждом запросе и обнуляет `SafeJoin`. `LD_PRELOAD/PATH` через `Env`.
- [ ] Исправление:
  - [ ] `Dir` только внутри `DataDir` + каноникализация (`Abs+Clean+EvalSymlinks`, проверка префикса).
  - [ ] Allowlist бинарей (абсолютные пути + `LookPath`), запрет `sh -c` с произвольным кодом или явный флаг-привилегия.
  - [ ] `Env` allowlist (`JAVA_*` и т.п.), запрет `LD_*/LIBRARY_PATH/PYTHONPATH/PATH/JAVA_TOOL_OPTIONS`.
  - [ ] `Registry.Bind` при `Start`, `For(id)` без сетевого `Dir`, mismatch → error.
  - [ ] Валидация абсолютности/каноники `Dir/Cmd` на master (`instances_handlers.go:66-128`).
- [ ] Проверка: `Dir:"/"`, `Dir:"/etc"`, `Env:["LD_PRELOAD=..."]` отклоняются на мастере и на агенте.

### 5. Обход корня через симлинки в fsmgr
- [ ] Файл: `agent/internal/fsmgr/fsmgr.go:62-82, 93-118, 247-259`
- [ ] Проблема: только лексическая проверка, без `EvalSymlinks/O_NOFOLLOW`. `link → /etc` дает чтение/удаление вне корня. `Read` открывает FIFO/device. TOCTOU `Stat→Open/RemoveAll`.
- [ ] Исправление:
  - [ ] `EvalSymlinks` после резолва + проверка что итог внутри `Root`.
  - [ ] `O_NOFOLLOW` для родительских компонент (или запрет следовать симлинкам в родителях).
  - [ ] `Read`: только `Mode().IsRegular()`.
  - [ ] `Delete`: запрет `RemoveAll` через симлинк, проверка каждого компонента.
  - [ ] `MkdirAll(Dir(full))` — проверять родителей на симлинки.
- [ ] Проверка: тесты с симлинком `root/link → /etc` для `List/Read/Write/Mkdir/Delete` — все отклоняются.

### 6. install.sh: supply-chain + `chown -R /`
- [ ] Файл: `scripts/install.sh:61-86, 231-238`
- [ ] Проблема: `curl` Go и исходников без SHA256/sig, URL переопределяются через env. `chown -R "$data_dir"` без каноникализации — `"/"`, `/etc` или симлинк убивают систему.
- [ ] Исправление:
  - [ ] Проверка SHA256 для Go-тарбола и исходников (pin хеша, `--proto =https`).
  - [ ] Убрать env-переопределение `REPO_URL/BRANCH/GO_VERSION` или allowlist.
  - [ ] `realpath --canonicalize` + allowlist для `data_dir` (`/var/lib/panel-*`), `O_NOFOLLOW`, отказ при `/`, `/etc`, `/var`.
- [ ] Проверка: подмена URL не проходит, `data_dir=/` отклоняется.

### 7. Пароль/токен в `ps` и shell-инъекция
- [ ] Файлы: `master/cmd/master/main.go:113-124`, `agent/cmd/agent/main.go:26-30`, `agent/internal/config/config.go:27-28`, `scripts/install.sh:17-23, 231`
- [ ] Проблема: `-create-admin user:pass` и `-token` видны в `ps`/history/journal. `%q` (bash) под `/bin/sh` (dash) + раскрытие `$()``` `` `!`.
- [ ] Исправление:
  - [ ] Пароль только через env/file/stdin (`-create-admin-file`, `PANEL_ADMIN_PASS`), токен только через файл/env `0600`.
  - [ ] `read -s` без эха, мин. длина 6 → 12+.
  - [ ] Убрать `%q/su`-конструкцию, использовать `runuser/setpriv` или корректное квотирование под sh.
- [ ] Проверка: секрета нет в `ps`, в логах — только маска.

---

## P1 — High (второй заход)

### 8. Отравление чужих инстансов скомпрометированным агентом
- [ ] Файлы: `master/internal/api/api.go:111-139`, `master/internal/agent/agent.go:211-241`
- [ ] Исправление: проверка `InstanceID ∈ nodeID` (через `DB.GetInstance` + `NodeID`) перед `Broadcast/Store/Push`. `SetReadLimit` на `Read`, rate-limit на `KindEvent`.
- [ ] Проверка: агент A не может пушить консоль/статус/метрики инстанса ноды B.

### 9. Login/пароли без rate-limit, JWT без отзыва
- [ ] Файлы: `master/internal/api/auth_handlers.go:24-47, 67-70`, `master/internal/auth/auth.go:100-146`, `master/internal/config/config.go:31`
- [ ] Исправление:
  - [ ] Rate-limit + lockout на `login`, `agentWS`, `changePassword` (non-admin ветка).
  - [ ] TTL 24ч → 1-2ч (конфигурируемо), валидация `token-ttl > 0`.
  - [ ] Отзыв: `jti` deny-list или версия токена (инвалидация при смене пароля/роли/удалении, при `logout`).
  - [ ] `Sscanf(sub)` с проверкой ошибки, валидация `Issuer/Role/Subject`, запрет `ID=0`.
  - [ ] Не отдавать токен для `localStorage` без нужды; задокументировать хранение (httpOnly cookie предпочтительно).
- [ ] Проверка: брутфорс режется, старый JWT после смены пароля невалиден.

### 10. RCE-конфиг доступен non-admin через start
- [ ] Файл: `master/internal/api/instances_handlers.go:66-128, 202-235, 287-299`
- [ ] Исправление: строгая валидация `Name (len+charset)`, `Dir` (absolute+canonical, без `..`), `Cmd` (non-empty, без NUL), `Env` (`k=v`, без `=`/`\n`/`\0` в ключе, лимит числа/размера). Идемпотентность power-операций, аудит параметров.
- [ ] Проверка: `Dir=/etc`, `Cmd=["sh","-c","..."]`, `LD_PRELOAD` отклоняются или требуют отдельной привилегии.

### 11. Файловый прокси: валидация + аудит + ошибки
- [ ] Файл: `master/internal/api/files_handlers.go:12-106`
- [ ] Исправление: `Clean` + reject `..`/absolute/NUL/лимит длины `Path` на master; лимит/квота `ContentB64`; `AddAudit` на read/write/mkdir/delete; generic `"agent error"` наружу, детали в лог.
- [ ] Проверка: `Path:"../../etc/passwd"` режется на мастере, файловые ops в audit-логе.

### 12. WS-консоль: лимиты и аудит
- [ ] Файл: `master/internal/ws/console.go:100-119`
- [ ] Исправление: лимит длины `input` (напр. 4-8KB), rate-limit на соединение, аудит `console.input` (privileged-op).
- [ ] Проверка: флуд stdin не проходит.

### 13. Agent client: лимиты, утечки, метрики
- [ ] Файл: `agent/internal/client/client.go:71-156, 229-308`, `agent/internal/metrics/metrics.go:37-99`
- [ ] Исправление:
  - [ ] `SetReadLimit`, max-inflight + rate-limit, валидация `ID`.
  - [ ] Разделить `writeMu` для ответов и эвентов или очередь с таймаутом.
  - [ ] Ключ консоли `(instanceID, connectionID)`, реальный `Detach` (закрыть `ch`).
  - [ ] Починить `backoff` (убрать недостижимый `if err==nil`), `metrics Start/Stop` через `context` вместо `sync.Once`, разделить `sample(emit bool)`, rate-limit `metrics.snapshot`, проверка `pid+createTime`.
  - [ ] Обработка `Ping` от мастера.
- [ ] Проверка: флуд большими `Message` не кладет агента, повторный коннект не убивает метрики, `attach`-спам не течет.

### 14. Runner: изоляция и гонки
- [ ] Файлы: `agent/internal/runner/runner.go:102-310`, `proc_unix.go:17-23`, `proc_windows.go:12-17`
- [ ] Исправление:
  - [ ] `Restart` под одним `mu` (без отпуска между `Stop/Start`), защита от дублей.
  - [ ] `Close(stdin)`, закрытие pipe на `Detach`, лимит `ring.snapshot()` amplification.
  - [ ] `SIGTERM → SIGKILL` (graceful), Job Objects на Windows для дерева процессов.
  - [ ] Лимиты: макс. инстансов, CPU/mem (cgroup где есть).
  - [ ] Привязка PID через `startTime/createTime` для метрик.
  - [ ] Предупреждение/отказ при `euid==0`, `LookPath` для бинаря.
- [ ] Проверка: конкурентные `Start/Stop/Restart` не плодят дубли, дети убиваются на обеих ОС.

### 15. systemd units и hardening
- [ ] Файлы: `deploy/systemd/*.service`
- [ ] Исправление: первая строка `--` → `#`, `systemd-analyze verify`. Агенту `ProtectSystem=strict + ReadWritePaths=/var/lib/panel-agent /etc/panel`, `ProtectHome/ProtectKernelTunables/RestrictSUIDSGID/SystemCallFilter/MemoryMax`. `Restart=on-failure + StartLimit*` вместо `always`.
- [ ] Проверка: `verify` чисто, агент с RCE пишет только в свои пути.

---

## P2 — Medium (закалка)

- [ ] `db/users.go:74-83` — `SetPassword/SetRole` проверять `RowsAffected`, иначе `ErrNotFound` (убрать ложный `ok:true`).
- [ ] `db/users.go:131-133` — валидировать `IDs` в `SetUserInstances` заранее (сейчас `INSERT OR IGNORE` тихо дропает несуществующие).
- [ ] `db/audit.go:16-20` — не глотать ошибку `AddAudit`, лимиты длины полей.
- [ ] `db/db.go:21` — DSN `file:%s` экранировать/валидировать путь `DataDir`.
- [ ] `db/*` — `SetRole` проверять allow-list ролей (defense-in-depth, не только в хендлерах).
- [ ] `master/internal/api/users_handlers.go:23-52` — charset для username (сейчас только длина), явный DTO вместо возврата `*db.User` (хрупкая связь с `json:"-"`).
- [ ] `master/internal/api/users_handlers.go:142-183` — re-auth/аудит при смене пароля админом, rate-limit на подбор `currentPassword`.
- [ ] `master/internal/api/nodes_handlers.go:81-100` — rate-limit + аудит failed-attempts на `agentWS`, API ротации/отзыва токена ноды без удаления.
- [ ] `master/internal/agent/agent.go:46-84` — убрать `OriginPatterns:["*"]` для `/api/agent/ws`, rotation токенов, защита от supersede-кика (алерт при вытеснении соединения).
- [ ] `master/internal/api/api.go:168-203, 295-321` — `readJSON` с `DisallowUnknownFields` + лимит глубины, `instanceFrom` не возвращать пустой `&db.Instance{}` (лучше ошибка), CORS/rate-limit/timeout middleware.
- [ ] `master/internal/console/console.go` + `metrics` — `DropNode` при удалении ноды (сейчас только `dropInstanceState`).
- [ ] `agent/internal/config/config.go:64-93` — валидация схемы `MasterURL` (только `ws/wss`), проверка владельца/прав `agent.json`, запрет относительного `DataDir` по умолчанию или каноникализация.
- [ ] `agent/internal/fsmgr/fsmgr.go:97-159, 196-233` — убрать `TrimSpace`/запрет `:` (ломает легитимные имена), квота на число файлов, пагинация `List`, явный `Chmod 0644` после `Rename`.
- [ ] `scripts/install.sh:17-23, 131-165, 235-236` — JSON через `jq`/`python3 json.dump`, пароль `read -s`, дока про reverse-proxy/TLS вместо голого `http://IP:8080`.
- [ ] `go.mod` — сверить `GO_VERSION` с `install.sh` (1.23.0 vs 1.23.12), решить гэп AGENTS.md (`systemd/dbus, docker, pty` заявлены, но агент — голый `exec`).

---

## P3 — Тесты (после P0-P1)

Сейчас тесты только в `master/internal/auth` (happy-path + expiry/wrong-secret). Нет покрытия IDOR, `SafeJoin`, `For`, WS, runner.

- [ ] `auth`: подмена `alg`, пустой `sub`, произвольная `role`, `ID=0`, отзыв после смены роли/пароля.
- [ ] `api`: `listUserInstances` IDOR (два юзера, перекрестные запросы), `changePassword`/`setUserInstances` RBAC-матрица.
- [ ] `ws`: Origin-матрица (`*` отклоняется), permission-матрица (чужой инстанс → 403).
- [ ] `fsmgr`: симлинк-матрица (`link → /etc` для List/Read/Write/Mkdir/Delete), FIFO/device в `Read`, `..`/absolute/NUL/`C:\`.
- [ ] `runner/client`: `Dir:"/"`, `LD_PRELOAD` в `Env`, конкурентные `Start/Stop/Restart`, `attach`-спам (нет утечек), повторный коннект метрик.
- [ ] `agent event`: агент A пушит `InstanceID` ноды B → отклоняется.
- [ ] Добавить `govulncheck ./...` и `go list -m -u` в CI, обновить `modernc/sqlite`, `gopsutil`.

---

## P4 — Финальная проверка (перед закрытием)

По AGENTS.md «Проверка перед завершением задачи»:

- [ ] `gofmt` / `golangci-lint` чисто.
- [ ] `go vet ./...` чисто.
- [ ] `go test ./...` зелено.
- [ ] Оба бинарника собираются (`panel-master`, `panel-agent`).
- [ ] Фронт-шаблоны и `static/js` (alpine, xterm, uplot, app/store/api) вшиваются через `embed`, страницы `/login /dashboard /nodes /instances /users /settings` открываются вручную.
- [ ] Связанные API проверены (auth/users/nodes/instances/files/metrics/audit + WS консоль).
- [ ] Permissions перепроверены для всех измененных privileged-операций.
- [ ] Ошибки возвращаются в предсказуемом формате, агентские детали не светятся наружу.
- [ ] Секреты не попали в код, логи, frontend, systemd units, shell-history.
- [ ] Нет временных решений/mocks/TODO вместо функциональности.
- [ ] Если тест невозможен — указать причину явно.

---

## Приоритет (при конфликте требований)

Безопасность → корректность/сохранность данных → надежность Master/Agent → производительность → простота → UX → фичи.
