package main

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"time"
)

// statusWriter запоминает код ответа для журнала.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// logRequests пишет в журнал каждую обработанную операцию.
func (a *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}

		attrs := []any{
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "duration_ms", time.Since(start).Milliseconds(), "remote", r.RemoteAddr,
		}
		switch {
		case sw.status >= 500:
			a.log.Error("запрос", attrs...)
		case sw.status >= 400:
			a.log.Warn("запрос", attrs...)
		default:
			a.log.Info("запрос", attrs...)
		}
	})
}

// recoverPanic не даёт сервису упасть из-за паники в обработчике: клиент получает 500.
func (a *App) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.log.Error("паника в обработчике", "method", r.Method, "path", r.URL.Path,
					"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal_error", "внутренняя ошибка сервиса", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// probeWriter принимает ответ стандартного обработчика, не отправляя его клиенту.
type probeWriter struct {
	header http.Header
	status int
}

func (p *probeWriter) Header() http.Header         { return p.header }
func (p *probeWriter) Write(b []byte) (int, error) { return len(b), nil }
func (p *probeWriter) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
	}
}

// jsonFallback заменяет текстовые ответы 404 и 405 стандартного маршрутизатора
// на JSON в общем формате ошибок.
func jsonFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}

		probe := &probeWriter{header: http.Header{}}
		handler.ServeHTTP(probe, r)
		if probe.status == http.StatusMethodNotAllowed {
			if allow := probe.header.Get("Allow"); allow != "" {
				w.Header().Set("Allow", allow)
			}
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
				"метод "+r.Method+" не поддерживается для "+r.URL.Path, nil)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "маршрут "+r.URL.Path+" не найден", nil)
	})
}
