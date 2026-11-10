# tasks-service

Учебный микросервис «Список задач»: REST API на Go, данные в PostgreSQL, запуск в Docker.

Автор: Скрябинский Виктор, группа 8К33 (`8k33-23-skryabinsky`).

## Запуск

Нужны только Docker и Docker Compose.

```bash
git clone https://github.com/vos22350/tasks-service.git
cd tasks-service
docker compose up -d --build
```

Сервис поднимется на `http://localhost:8000`, СУБД — в соседнем контейнере (наружу не публикуется).
Параметры (имя базы, пароль, порт) можно изменить в файле `.env` — см. `.env.example`.

```bash
curl http://localhost:8000/health
# {"status":"ok","database":"up"}
```

Остановка: `docker compose down` (с удалением данных — `docker compose down -v`).

## API

| Метод | Путь | Что делает | Успех |
|---|---|---|---|
| GET | `/health` | состояние сервиса и базы | 200 |
| GET | `/api/v1/tasks` | список задач; параметры `status`, `limit` (1–100), `offset` | 200 |
| POST | `/api/v1/tasks` | создать задачу | 201 |
| GET | `/api/v1/tasks/{id}` | получить задачу | 200 |
| PUT | `/api/v1/tasks/{id}` | заменить задачу целиком | 200 |
| PATCH | `/api/v1/tasks/{id}` | изменить отдельные поля | 200 |
| DELETE | `/api/v1/tasks/{id}` | удалить задачу | 204 |
| GET | `/swagger` | Swagger UI | 200 |
| GET | `/openapi.yaml` | описание API (OpenAPI 3.0) | 200 |

Поля задачи:

| Поле | Тип | Ограничения |
|---|---|---|
| `title` | строка | обязательное, 1–200 символов, уникальное |
| `description` | строка | до 2000 символов, по умолчанию пустая |
| `status` | строка | `todo` (по умолчанию), `in_progress`, `done` |
| `priority` | целое | от 1 до 5, по умолчанию 3 |
| `due_date` | строка | дата `ГГГГ-ММ-ДД` или `null` |

Пример:

```bash
curl -X POST http://localhost:8000/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"title": "Сдать ЛР", "priority": 1, "due_date": "2026-12-01"}'
```

## Ошибки

При некорректном запросе сервис не падает, а возвращает код ошибки и JSON с пояснением:

```json
{
  "error": "validation_error",
  "message": "данные не прошли проверку",
  "details": { "title": "не может быть пустым", "priority": "целое число от 1 до 5" }
}
```

| Код | `error` | Когда |
|---|---|---|
| 400 | `bad_request` | битый JSON, неверный тип поля, неизвестное поле, нечисловой `id`, неверный параметр запроса |
| 404 | `not_found` | задачи или маршрута нет |
| 405 | `method_not_allowed` | метод не поддерживается для этого пути |
| 409 | `conflict` | задача с таким названием уже есть |
| 413 | `payload_too_large` | тело запроса больше 1 МБ |
| 415 | `unsupported_media_type` | тело не `application/json` |
| 422 | `validation_error` | поля не прошли проверку (подробности в `details`) |
| 500 | `internal_error` | внутренняя ошибка; подробности только в журнале |

## Журнал

Сервис пишет журнал в stdout в формате JSON: каждый запрос (метод, путь, код, длительность),
действия над задачами и ошибки. Просмотр: `docker compose logs -f app`.

## Проверка в Postman

Коллекция: `postman/tasks-service.postman_collection.json` (22 запроса с проверками).
В Postman: **Import** → выбрать файл → **Run collection**. Адрес сервиса задаётся переменной `baseUrl`.

Та же коллекция из командной строки (Newman):

```bash
docker run --rm --network host -v "$PWD/postman:/etc/newman" postman/newman:alpine \
  run tasks-service.postman_collection.json
```

## Устройство

```
main.go         запуск, подключение к базе, корректное завершение
handlers.go     маршруты и обработчики запросов
validate.go     проверка входных данных
store.go        SQL-запросы к PostgreSQL
middleware.go   журнал запросов, перехват паник, ответы 404/405 в JSON
main_test.go    тесты проверки данных и обработки ошибок
openapi.yaml    описание API
Dockerfile      двухэтапная сборка (golang → alpine)
docker-compose.yml  сервис + PostgreSQL
```

Образ собирается в два этапа: в первом (`golang:1.23-alpine`) выполняются `go vet`, тесты и компиляция,
во второй (`alpine:3.20`) копируется только готовый бинарный файл. Сервис работает от непривилегированного пользователя.
