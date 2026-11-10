package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

var (
	// ErrNotFound — задачи с таким id нет.
	ErrNotFound = errors.New("task not found")
	// ErrConflict — нарушено ограничение уникальности (название уже занято).
	ErrConflict = errors.New("task title already exists")
)

// Task — задача в том виде, в каком она отдаётся клиенту.
type Task struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Priority    int       `json:"priority"`
	DueDate     *string   `json:"due_date"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Store — слой доступа к PostgreSQL.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
    id          BIGSERIAL    PRIMARY KEY,
    title       VARCHAR(200) NOT NULL UNIQUE,
    description TEXT         NOT NULL DEFAULT '',
    status      VARCHAR(20)  NOT NULL DEFAULT 'todo'
                CHECK (status IN ('todo', 'in_progress', 'done')),
    priority    SMALLINT     NOT NULL DEFAULT 3 CHECK (priority BETWEEN 1 AND 5),
    due_date    DATE,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tasks_status_idx ON tasks (status);
`

const columns = `id, title, description, status, priority, to_char(due_date, 'YYYY-MM-DD'), created_at, updated_at`

// Migrate создаёт таблицу при первом запуске.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Ping проверяет соединение с базой (для /health).
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (Task, error) {
	var t Task
	var due sql.NullString
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.Priority, &due, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, mapError(err)
	}
	if due.Valid {
		t.DueDate = &due.String
	}
	return t, nil
}

// mapError переводит ошибки PostgreSQL в ошибки предметной области.
func mapError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" { // unique_violation
		return ErrConflict
	}
	return err
}

func nullDate(d *string) any {
	if d == nil || *d == "" {
		return nil
	}
	return *d
}

// List возвращает страницу задач и общее их количество с учётом фильтра.
func (s *Store) List(ctx context.Context, status string, limit, offset int) ([]Task, int, error) {
	where, args := "", []any{}
	if status != "" {
		where = " WHERE status = $1"
		args = append(args, status)
	}

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM tasks"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf("SELECT %s FROM tasks%s ORDER BY id LIMIT $%d OFFSET $%d",
		columns, where, len(args)+1, len(args)+2)
	rows, err := s.db.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, t)
	}
	return items, total, rows.Err()
}

// Get возвращает задачу по id.
func (s *Store) Get(ctx context.Context, id int64) (Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM tasks WHERE id = $1", id))
}

// Create добавляет задачу. Все поля в in уже проверены и заполнены значениями по умолчанию.
func (s *Store) Create(ctx context.Context, in TaskInput) (Task, error) {
	return scanTask(s.db.QueryRowContext(ctx,
		`INSERT INTO tasks (title, description, status, priority, due_date)
		 VALUES ($1, $2, $3, $4, $5) RETURNING `+columns,
		*in.Title, *in.Description, *in.Status, *in.Priority, nullDate(in.DueDate)))
}

// Replace полностью заменяет задачу (PUT).
func (s *Store) Replace(ctx context.Context, id int64, in TaskInput) (Task, error) {
	return scanTask(s.db.QueryRowContext(ctx,
		`UPDATE tasks SET title = $1, description = $2, status = $3, priority = $4, due_date = $5, updated_at = now()
		 WHERE id = $6 RETURNING `+columns,
		*in.Title, *in.Description, *in.Status, *in.Priority, nullDate(in.DueDate), id))
}

// Patch меняет только переданные поля (PATCH).
func (s *Store) Patch(ctx context.Context, id int64, in TaskInput) (Task, error) {
	sets, args := []string{}, []any{}
	add := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if in.Title != nil {
		add("title", *in.Title)
	}
	if in.Description != nil {
		add("description", *in.Description)
	}
	if in.Status != nil {
		add("status", *in.Status)
	}
	if in.Priority != nil {
		add("priority", *in.Priority)
	}
	if in.DueDate != nil {
		add("due_date", nullDate(in.DueDate))
	}
	args = append(args, id)
	query := fmt.Sprintf("UPDATE tasks SET %s, updated_at = now() WHERE id = $%d RETURNING %s",
		strings.Join(sets, ", "), len(args), columns)
	return scanTask(s.db.QueryRowContext(ctx, query, args...))
}

// Delete удаляет задачу.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM tasks WHERE id = $1", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
