package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestValidate(t *testing.T) {
	cases := []struct {
		name         string
		in           TaskInput
		requireTitle bool
		wantFields   []string
	}{
		{"корректная задача", TaskInput{Title: ptr("Сдать ЛР"), Priority: ptr(2), DueDate: ptr("2026-12-01")}, true, nil},
		{"нет названия", TaskInput{}, true, []string{"title"}},
		{"название из пробелов", TaskInput{Title: ptr("   ")}, true, []string{"title"}},
		{"длинное название", TaskInput{Title: ptr(strings.Repeat("я", 201))}, true, []string{"title"}},
		{"название из 200 символов", TaskInput{Title: ptr(strings.Repeat("я", 200))}, true, nil},
		{"неверный статус", TaskInput{Title: ptr("a"), Status: ptr("closed")}, true, []string{"status"}},
		{"приоритет вне диапазона", TaskInput{Title: ptr("a"), Priority: ptr(9)}, true, []string{"priority"}},
		{"несуществующая дата", TaskInput{Title: ptr("a"), DueDate: ptr("2026-02-30")}, true, []string{"due_date"}},
		{"пустая дата снимает срок", TaskInput{DueDate: ptr("")}, false, nil},
		{"patch без названия", TaskInput{Status: ptr("done")}, false, nil},
		{"несколько ошибок", TaskInput{Title: ptr(""), Status: ptr("x"), Priority: ptr(0)}, true, []string{"title", "status", "priority"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := c.in.validate(c.requireTitle)
			if len(errs) != len(c.wantFields) {
				t.Fatalf("ошибки %v, ожидались поля %v", errs, c.wantFields)
			}
			for _, f := range c.wantFields {
				if _, ok := errs[f]; !ok {
					t.Errorf("нет ошибки для поля %q: %v", f, errs)
				}
			}
		})
	}
}

func TestValidateTrimsTitle(t *testing.T) {
	in := TaskInput{Title: ptr("  Сдать ЛР  ")}
	if errs := in.validate(true); len(errs) != 0 {
		t.Fatalf("неожиданные ошибки: %v", errs)
	}
	if *in.Title != "Сдать ЛР" {
		t.Errorf("название не обрезано: %q", *in.Title)
	}
}

func TestApplyDefaults(t *testing.T) {
	in := TaskInput{Title: ptr("a")}
	in.applyDefaults()
	if *in.Status != "todo" || *in.Priority != 3 || *in.Description != "" {
		t.Errorf("значения по умолчанию: status=%q priority=%d description=%q", *in.Status, *in.Priority, *in.Description)
	}
}

// Проверки ниже не доходят до базы данных: запрос отклоняется раньше.
func TestRequestErrors(t *testing.T) {
	app := &App{store: &Store{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	cases := []struct {
		name, method, path, contentType, body string
		wantStatus                            int
		wantError                             string
	}{
		{"неизвестный маршрут", "GET", "/api/v1/unknown", "", "", 404, "not_found"},
		{"метод не поддерживается", "DELETE", "/api/v1/tasks", "", "", 405, "method_not_allowed"},
		{"нечисловой id", "GET", "/api/v1/tasks/abc", "", "", 400, "bad_request"},
		{"отрицательный id", "DELETE", "/api/v1/tasks/-5", "", "", 400, "bad_request"},
		{"не JSON", "POST", "/api/v1/tasks", "text/plain", "title=a", 415, "unsupported_media_type"},
		{"битый JSON", "POST", "/api/v1/tasks", "application/json", `{"title": "a"`, 400, "bad_request"},
		{"пустое тело", "POST", "/api/v1/tasks", "application/json", "", 400, "bad_request"},
		{"неверный тип", "POST", "/api/v1/tasks", "application/json", `{"title": "a", "priority": "high"}`, 400, "bad_request"},
		{"неизвестное поле", "POST", "/api/v1/tasks", "application/json", `{"title": "a", "owner": "me"}`, 400, "bad_request"},
		{"два объекта", "POST", "/api/v1/tasks", "application/json", `{"title": "a"}{"title": "b"}`, 400, "bad_request"},
		{"ошибка проверки", "POST", "/api/v1/tasks", "application/json", `{"title": "", "status": "x"}`, 422, "validation_error"},
		{"пустой patch", "PATCH", "/api/v1/tasks/1", "application/json", `{}`, 400, "bad_request"},
		{"неверный limit", "GET", "/api/v1/tasks?limit=1000", "", "", 400, "bad_request"},
		{"неверный статус в фильтре", "GET", "/api/v1/tasks?status=closed", "", "", 400, "bad_request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if c.contentType != "" {
				req.Header.Set("Content-Type", c.contentType)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != c.wantStatus {
				t.Fatalf("код %d, ожидался %d", resp.StatusCode, c.wantStatus)
			}
			var body apiError
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("ответ не JSON: %v", err)
			}
			if body.Error != c.wantError || body.Message == "" {
				t.Errorf("тело ошибки %+v, ожидался код %q", body, c.wantError)
			}
		})
	}
}
