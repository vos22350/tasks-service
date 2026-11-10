# syntax=docker/dockerfile:1

# ---------- Этап 1: сборка ----------
# Полный образ с компилятором Go; в итоговый образ он не попадает.
FROM golang:1.23-alpine AS build
WORKDIR /src

# Сначала только описание зависимостей: слой с модулями кэшируется,
# пока go.mod и go.sum не меняются.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Проверки и тесты выполняются при сборке: образ с неработающим кодом не соберётся.
RUN go vet ./... && go test ./...

# Статический бинарный файл без отладочной информации.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tasks-service .

# ---------- Этап 2: запуск ----------
# Минимальный образ: только бинарный файл и непривилегированный пользователь.
FROM alpine:3.20
RUN adduser -D -H -u 10001 app
COPY --from=build /out/tasks-service /usr/local/bin/tasks-service
USER app
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -qO- http://127.0.0.1:8000/health >/dev/null || exit 1
ENTRYPOINT ["tasks-service"]
