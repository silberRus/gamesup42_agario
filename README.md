# Первый этап отбора

Многопользовательская браузерная арена: клетки едят токены и друг друга, выживает сильнейший кандидат.
Тестовое задание для GamesUp42. Автор — Андрей Карецкий.

- Сервер на Go 1.22: авторитетная симуляция 25 тиков/с, WebSocket, история сессий в SQLite.
- Клиент на чистом JS и Canvas, без фреймворков и сборки. Статика вшита в бинарник через `go:embed`.
- Админка `/admin`: игроки, история сессий, графики нагрузки, управление ботами и миром.
- На выходе один исполняемый файл без внешних зависимостей (SQLite на чистом Go, cgo не нужен).

## Локальный запуск

Нужен Go 1.22 или новее.

```bash
go run .
```

Игра откроется на http://localhost:8080, админка — на http://localhost:8080/admin
(логин любой, пароль из `ADMIN_PASSWORD`). Если пароль не задан, сервер придумает временный
и напишет его в лог при старте.

С параметрами (bash):

```bash
PORT=9000 ADMIN_PASSWORD=secret go run .
```

То же в PowerShell:

```powershell
$env:PORT = "9000"; $env:ADMIN_PASSWORD = "secret"; go run .
```

Проверка кода:

```bash
go build ./... && go vet ./...
```

## Переменные окружения

| Переменная       | По умолчанию  | Назначение                                                                       |
|------------------|---------------|----------------------------------------------------------------------------------|
| `PORT`           | `8080`        | Порт HTTP-сервера                                                                |
| `HOST`           | все адреса    | Адрес для прослушивания; за прокси — `127.0.0.1`, чтобы порт не торчал наружу    |
| `BASE_PATH`      | —             | Префикс URL, если игра живёт не в корне домена, например `/gamesup42-otbor-karetsky` |
| `ADMIN_PASSWORD` | случайный     | Пароль админки (Basic Auth, логин любой)                                         |
| `DB_PATH`        | `sessions.db` | Путь к файлу SQLite с историей сессий                                            |
| `TOKENS`         | —             | Число для подписи «Собрано с AI за … токенов» на экране входа; без него скрыта   |
| `REPO_URL`       | —             | Ссылка «GitHub» на экране входа; без неё скрыта                                  |

## Сборка под Linux (amd64)

Bash (Linux, macOS, Git Bash):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o agario .
```

PowerShell:

```powershell
$env:CGO_ENABLED = "0"; $env:GOOS = "linux"; $env:GOARCH = "amd64"; go build -ldflags="-s -w" -o agario .
```

Получится файл `agario` — его достаточно для запуска на сервере.

## Деплой на VPS

Ниже — Debian/Ubuntu, домен `game.example.com` уже указывает на сервер.

1. Подготовить сервер (один раз):

   ```bash
   sudo useradd --system --home /opt/agario --shell /usr/sbin/nologin agario
   sudo mkdir -p /opt/agario
   ```

2. Скопировать бинарник и конфиги со своей машины:

   ```bash
   scp agario deploy/agario.service deploy/Caddyfile user@your-server:/tmp/
   ```

3. Разложить файлы и задать переменные окружения:

   ```bash
   sudo install -o agario -g agario -m 755 /tmp/agario /opt/agario/agario
   sudo cp /tmp/agario.service /etc/systemd/system/agario.service
   sudo tee /opt/agario/.env >/dev/null <<'EOF'
   PORT=8080
   ADMIN_PASSWORD=change-me-to-a-long-password
   DB_PATH=/opt/agario/sessions.db
   TOKENS=1234567
   REPO_URL=https://github.com/user/repo
   EOF
   sudo chown agario:agario /opt/agario/.env && sudo chmod 600 /opt/agario/.env
   sudo chown agario:agario /opt/agario
   ```

4. Включить сервис:

   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now agario
   sudo systemctl status agario
   ```

5. Поставить [Caddy](https://caddyserver.com/docs/install), вписать свой домен в `Caddyfile` и включить его:

   ```bash
   sudo cp /tmp/Caddyfile /etc/caddy/Caddyfile
   sudo systemctl reload caddy
   ```

   Caddy сам получит сертификат и будет проксировать HTTPS и WebSocket на `127.0.0.1:8080`.

6. Закрыть прямой доступ к порту приложения, оставив только 80 и 443. Сервер доверяет заголовку
   `X-Forwarded-For`, поэтому снаружи должен быть доступен только Caddy:

   ```bash
   sudo ufw allow 22,80,443/tcp && sudo ufw enable
   ```

### Вариант: по пути на сайте за существующим nginx

Если на сервере уже есть сайт под nginx, Caddy не нужен (шаги 5–6 пропустить). В `.env` добавить:

```
HOST=127.0.0.1
BASE_PATH=/gamesup42-otbor-karetsky
```

и вставить блок из `deploy/nginx-location.conf` внутрь `server { ... }` сайта (тот, что слушает 443),
затем `sudo nginx -t && sudo systemctl reload nginx`. Игра откроется на
`https://ваш-сайт/gamesup42-otbor-karetsky/`, админка — на `.../admin`.

Обновление: скопировать новый бинарник в `/opt/agario/agario` и выполнить `sudo systemctl restart agario`.
При остановке сервис дописывает в историю сессии игроков, которые были онлайн.
Логи: `journalctl -u agario -f`.

## Админка

- **Игроки** — имя, бот или человек, IP, User-Agent, время входа, длительность, пинг, масса; кнопка «Выгнать».
- **«Отметить этот браузер как мой»** — ставит cookie. Игровые сессии, открытые после этого в том же
  браузере, помечаются «это я»; остальные люди получают пометку «🕵️ Похоже, это проверяющий».
- **История сессий** — последние 100 завершённых сессий из SQLite.
- **Нагрузка** — время тика (среднее и p99 за 10 секунд), CPU процесса (из `/proc/self/stat`; вне Linux — `n/a`),
  RAM, горутины, соединения, сообщения в секунду, исходящий трафик, число сущностей. Графики за 5 минут.
- **Управление** — число ботов (0–50), сложность ботов, сброс мира.

## Структура проекта

```
main.go      запуск, конфигурация из окружения, маршруты, вшитая статика
game.go      мир и тик: движение, поедание, сетка коллизий, боты, рассылка снимков, команды админки
client.go    WebSocket-соединение игрока: чтение ввода, запись, пинг
admin.go     админка: Basic Auth, cookie владельца, /admin/ws
stats.go     метрики нагрузки и их история за 5 минут
store.go     SQLite: история сессий
web/
  index.html экран входа, интерфейс игры
  game.js    сеть, интерполяция снимков, отрисовка, ввод
  admin.html разметка админки
  admin.js   таблицы, графики, управление
deploy/
  agario.service  unit для systemd
  Caddyfile       HTTPS и проксирование WebSocket
```

## Как это устроено

- Сервер — единственный источник правды. Клиент шлёт только точку, к которой движется клетка
  (смещение курсора от центра экрана), 20 раз в секунду.
- Каждый тик: движение и убывание массы → раскладка игроков по равномерной сетке (ячейка 100×100) →
  поедание токенов и клеток → решения ботов → рассылка снимков.
- Клетка съедает другую, если тяжелее минимум в 1,25 раза и накрывает её центр.
- Каждому клиенту уходит только то, что попадает в его область видимости, плюс топ-10. Область одинакова
  для любого экрана и растёт с массой.
- Клиент рисует мир с задержкой 100 мс, плавно интерполируя между двумя соседними снимками.
