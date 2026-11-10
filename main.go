// tasks-service — учебный микросервис «Список задач» (REST API + PostgreSQL).
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
)

// version задаётся при сборке: -ldflags "-X main.version=...".
var version = "dev"

// App хранит зависимости обработчиков.
type App struct {
	store *Store
	log   *slog.Logger
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// dsn собирает строку подключения из переменных окружения.
func dsn() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		env("DB_HOST", "localhost"), env("DB_PORT", "5432"), env("DB_USER", "tasks"),
		env("DB_PASSWORD", "tasks"), env("DB_NAME", "tasks"), env("DB_SSLMODE", "disable"))
}

// connect ждёт готовности СУБД: контейнер приложения может стартовать раньше неё.
func connect(log *slog.Logger) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lastErr = db.PingContext(ctx)
		cancel()
		if lastErr == nil {
			return db, nil
		}
		log.Warn("база данных пока недоступна", "attempt", attempt, "error", lastErr.Error())
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("не удалось подключиться к базе данных: %w", lastErr)
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	db, err := connect(log)
	if err != nil {
		log.Error("запуск прерван", "error", err.Error())
		os.Exit(1)
	}
	defer db.Close()

	store := &Store{db: db}
	if err := store.Migrate(context.Background()); err != nil {
		log.Error("не удалось создать схему", "error", err.Error())
		os.Exit(1)
	}
	log.Info("схема базы данных готова")

	app := &App{store: store, log: log}
	srv := &http.Server{
		Addr:              ":" + env("APP_PORT", "8000"),
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Info("сервис запущен", "addr", srv.Addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("ошибка HTTP-сервера", "error", err.Error())
			os.Exit(1)
		}
	}()

	// Корректное завершение по SIGTERM (docker stop) и Ctrl+C.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	sig := <-stop
	log.Info("получен сигнал, завершаю работу", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("ошибка при остановке", "error", err.Error())
	}
	log.Info("сервис остановлен")
}
