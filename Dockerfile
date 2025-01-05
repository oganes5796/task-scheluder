# Используем официальный образ Go
FROM golang:1.23 as builder

# Устанавливаем рабочую директорию
WORKDIR /app

# Копируем файлы в контейнер
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Сборка приложения
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

# Минимальный образ для продакшена
FROM alpine:latest

# Устанавливаем рабочую директорию
WORKDIR /root/

# Устанавливаем зависимости для работы
RUN apk add --no-cache tzdata

# Копируем приложение из builder
COPY --from=builder /app/main .
COPY config.yaml .

# Открываем порт
EXPOSE 8080

# Команда запуска приложения
CMD ["./main"]
