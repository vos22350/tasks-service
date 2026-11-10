package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed openapi.yaml
var openapiSpec []byte

//go:embed swagger.html
var swaggerPage []byte

// apiError — единый формат ответа об ошибке.
type apiError struct {
	Error   string            `json:"error"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string, details map[string]string) {
	writeJSON(w, status, apiError{Error: code, Message: message, Details: details})
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)
	mux.HandleFunc("GET /api/v1/tasks", a.listTasks)
	mux.HandleFunc("POST /api/v1/tasks", a.createTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}", a.getTask)
	mux.HandleFunc("PUT /api/v1/tasks/{id}", a.replaceTask)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", a.patchTask)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", a.deleteTask)
	mux.HandleFunc("GET /openapi.yaml", a.openapi)
	mux.HandleFunc("GET /swagger", a.swagger)
	return a.logRequests(a.recoverPanic(jsonFallback(mux)))
}

// internalError пишет подробности в журнал, а клиенту отдаёт общее сообщение.
func (a *App) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.log.Error("внутренняя ошибка", "method", r.Method, "path", r.URL.Path, "error", err.Error())
	writeError(w, http.StatusInternalServerError, "internal_error", "внутренняя ошибка сервиса", nil)
}

// storeError переводит ошибки хранилища в HTTP-ответы.
func (a *App) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "задача не найдена", nil)
	case errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "задача с таким названием уже существует", nil)
	default:
		a.internalError(w, r, err)
	}
}

// parseID читает идентификатор из пути. При ошибке сам отправляет 400.
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "идентификатор задачи должен быть положительным целым числом", nil)
		return 0, false
	}
	return id, true
}

// decodeBody разбирает JSON-тело запроса. При ошибке сам отправляет ответ и возвращает false.
func decodeBody(w http.ResponseWriter, r *http.Request, dst *TaskInput) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "ожидается Content-Type: application/json", nil)
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		var sizeErr *http.MaxBytesError
		switch {
		case errors.As(err, &sizeErr):
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "тело запроса больше 1 МБ", nil)
		case errors.Is(err, io.EOF):
			writeError(w, http.StatusBadRequest, "bad_request", "тело запроса пустое", nil)
		case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
			writeError(w, http.StatusBadRequest, "bad_request", "тело запроса не является корректным JSON", nil)
		case errors.As(err, &typeErr):
			writeError(w, http.StatusBadRequest, "bad_request", "неверный тип значения в поле "+typeErr.Field, nil)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			writeError(w, http.StatusBadRequest, "bad_request",
				"неизвестное поле "+strings.TrimPrefix(err.Error(), "json: unknown field "), nil)
		default:
			writeError(w, http.StatusBadRequest, "bad_request", "не удалось разобрать тело запроса", nil)
		}
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "bad_request", "тело запроса должно содержать один JSON-объект", nil)
		return false
	}
	return true
}

// GET /health
func (a *App) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		a.log.Error("база данных недоступна", "error", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "down", "version": version})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"service": "tasks-service", "status": "ok", "database": "up", "version": version})
}

// queryInt читает целочисленный параметр запроса в заданных границах.
func queryInt(r *http.Request, name string, def, min, max int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min || v > max {
		return 0, false
	}
	return v, true
}

// GET /api/v1/tasks?status=&limit=&offset=
func (a *App) listTasks(w http.ResponseWriter, r *http.Request) {
	details := map[string]string{}
	status := r.URL.Query().Get("status")
	if status != "" && !validStatus(status) {
		details["status"] = "допустимые значения: todo, in_progress, done"
	}
	limit, ok := queryInt(r, "limit", 20, 1, 100)
	if !ok {
		details["limit"] = "целое число от 1 до 100"
	}
	offset, ok := queryInt(r, "offset", 0, 0, 1_000_000)
	if !ok {
		details["offset"] = "целое неотрицательное число"
	}
	if len(details) > 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "некорректные параметры запроса", details)
		return
	}

	items, total, err := a.store.List(r.Context(), status, limit, offset)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

// POST /api/v1/tasks
func (a *App) createTask(w http.ResponseWriter, r *http.Request) {
	var in TaskInput
	if !decodeBody(w, r, &in) {
		return
	}
	if errs := in.validate(true); len(errs) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "данные не прошли проверку", errs)
		return
	}
	in.applyDefaults()

	task, err := a.store.Create(r.Context(), in)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	a.log.Info("задача создана", "id", task.ID, "title", task.Title)
	w.Header().Set("Location", "/api/v1/tasks/"+strconv.FormatInt(task.ID, 10))
	writeJSON(w, http.StatusCreated, task)
}

// GET /api/v1/tasks/{id}
func (a *App) getTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	task, err := a.store.Get(r.Context(), id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// PUT /api/v1/tasks/{id} — полная замена
func (a *App) replaceTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in TaskInput
	if !decodeBody(w, r, &in) {
		return
	}
	if errs := in.validate(true); len(errs) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "данные не прошли проверку", errs)
		return
	}
	in.applyDefaults()

	task, err := a.store.Replace(r.Context(), id, in)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	a.log.Info("задача заменена", "id", task.ID)
	writeJSON(w, http.StatusOK, task)
}

// PATCH /api/v1/tasks/{id} — частичное изменение
func (a *App) patchTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in TaskInput
	if !decodeBody(w, r, &in) {
		return
	}
	if in.isEmpty() {
		writeError(w, http.StatusBadRequest, "bad_request", "не передано ни одного поля для изменения", nil)
		return
	}
	if errs := in.validate(false); len(errs) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "данные не прошли проверку", errs)
		return
	}

	task, err := a.store.Patch(r.Context(), id, in)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	a.log.Info("задача изменена", "id", task.ID, "status", task.Status)
	writeJSON(w, http.StatusOK, task)
}

// DELETE /api/v1/tasks/{id}
func (a *App) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := a.store.Delete(r.Context(), id); err != nil {
		a.storeError(w, r, err)
		return
	}
	a.log.Info("задача удалена", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

// GET /openapi.yaml
func (a *App) openapi(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(openapiSpec)
}

// GET /swagger
func (a *App) swagger(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(swaggerPage)
}
