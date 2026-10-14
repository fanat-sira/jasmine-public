# ─── Этап 1: сборка бинаря ────────────────────────────────────────────────────
# Используем официальный Go-образ с Alpine для минимального размера.
FROM golang:1.26-alpine AS builder

# Устанавливаем необходимые для CGO зависимости (pgx не требует CGO, но gcc нужен на всякий случай).
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Сначала копируем только манифесты зависимостей — это кэширует слой с go mod download.
# Перекачка зависимостей произойдёт только при изменении go.mod / go.sum.
COPY go.mod go.sum ./
RUN go mod download

# Копируем весь исходный код и собираем бинарь.
COPY . .

# CGO_ENABLED=0 — статическая сборка без libc (работает в scratch/alpine без установки glibc).
# -ldflags="-s -w" — убираем debug-символы для уменьшения размера.
# -buildvcs=false — отключаем VCS stamping (актуально, если на сервере нет/недоступен .git).
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -buildvcs=false -ldflags="-s -w" -o /app/bin/jasmine ./cmd/jasmine

# Бинарь для одноразового импорта JSON-каталога упражнений.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -buildvcs=false -ldflags="-s -w" -o /app/bin/jasmine-import ./cmd/jasmine-import

# ─── Этап 2: финальный образ ─────────────────────────────────────────────────
FROM alpine:3.21

# Копируем CA-сертификаты (нужны для HTTPS-запросов к внешним API).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
# Копируем данные временных зон (нужны для time.LoadLocation).
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Создаём непривилегированного пользователя — best practice для продакшена.
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
USER appuser

WORKDIR /app

# Копируем только скомпилированный бинарь из builder-стадии.
COPY --from=builder /app/bin/jasmine .
COPY --from=builder /app/bin/jasmine-import .
COPY --from=builder /app/fitness_exercises_catalog_ru_en.json .

EXPOSE 8080

# Команда запуска — бинарь уже содержит все зависимости.
CMD ["./jasmine"]
