package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	worker "github.com/contribsys/faktory_worker_go"
)

func main() {
	rec := newRecorder()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	defaultWorker := worker.NewManager()
	defaultWorker.Concurrency = 2
	defaultWorker.ShutdownTimeout = 10 * time.Second
	registerDefaultJobs(defaultWorker, rec)
	defaultWorker.ProcessStrictPriorityQueues("default")

	reminderWorker := worker.NewManager()
	reminderWorker.Concurrency = 1
	reminderWorker.ShutdownTimeout = 15 * time.Second
	registerReminderJobs(reminderWorker, rec)
	reminderWorker.ProcessStrictPriorityQueues("reminders")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := defaultWorker.RunWithContext(ctx); err != nil && ctx.Err() == nil {
			log.Printf("default faktory worker stopped: %v", err)
			stop()
		}
	}()
	go func() {
		defer wg.Done()
		if err := reminderWorker.RunWithContext(ctx); err != nil && ctx.Err() == nil {
			log.Printf("reminder faktory worker stopped: %v", err)
			stop()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /processed", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, rec.list())
	})
	mux.HandleFunc("GET /processed/", func(w http.ResponseWriter, r *http.Request) {
		jid := strings.TrimPrefix(r.URL.Path, "/processed/")
		job, ok := rec.get(jid)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, job)
	})

	server := &http.Server{Addr: ":8090", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("probe api listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("probe api stopped: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("probe api shutdown: %v", err)
	}
	wg.Wait()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
