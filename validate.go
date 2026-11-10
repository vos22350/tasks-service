package main

import (
	"strings"
	"time"
	"unicode/utf8"
)

// TaskInput — тело запроса на создание и изменение задачи.
// Указатели позволяют отличить «поле не передано» от «передано пустое значение».
type TaskInput struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
	Priority    *int    `json:"priority"`
	DueDate     *string `json:"due_date"`
}

var allowedStatuses = map[string]bool{"todo": true, "in_progress": true, "done": true}

const (
	maxTitleLen       = 200
	maxDescriptionLen = 2000
)

func validStatus(s string) bool { return allowedStatuses[s] }

// validate проверяет переданные поля и возвращает ошибки по именам полей.
// requireTitle = true для POST и PUT, где название обязательно.
func (in *TaskInput) validate(requireTitle bool) map[string]string {
	errs := map[string]string{}

	if in.Title == nil {
		if requireTitle {
			errs["title"] = "обязательное поле"
		}
	} else {
		title := strings.TrimSpace(*in.Title)
		in.Title = &title
		switch {
		case title == "":
			errs["title"] = "не может быть пустым"
		case utf8.RuneCountInString(title) > maxTitleLen:
			errs["title"] = "не длиннее 200 символов"
		}
	}

	if in.Description != nil && utf8.RuneCountInString(*in.Description) > maxDescriptionLen {
		errs["description"] = "не длиннее 2000 символов"
	}

	if in.Status != nil && !validStatus(*in.Status) {
		errs["status"] = "допустимые значения: todo, in_progress, done"
	}

	if in.Priority != nil && (*in.Priority < 1 || *in.Priority > 5) {
		errs["priority"] = "целое число от 1 до 5"
	}

	// Пустая строка в due_date означает «убрать срок».
	if in.DueDate != nil && *in.DueDate != "" {
		if _, err := time.Parse("2006-01-02", *in.DueDate); err != nil {
			errs["due_date"] = "дата в формате ГГГГ-ММ-ДД"
		}
	}

	return errs
}

// applyDefaults подставляет значения по умолчанию для необязательных полей (POST и PUT).
func (in *TaskInput) applyDefaults() {
	if in.Description == nil {
		empty := ""
		in.Description = &empty
	}
	if in.Status == nil {
		status := "todo"
		in.Status = &status
	}
	if in.Priority == nil {
		priority := 3
		in.Priority = &priority
	}
}

// isEmpty сообщает, что в теле PATCH не передано ни одного поля.
func (in *TaskInput) isEmpty() bool {
	return in.Title == nil && in.Description == nil && in.Status == nil && in.Priority == nil && in.DueDate == nil
}
