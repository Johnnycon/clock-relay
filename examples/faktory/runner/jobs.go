package main

import (
	"context"
	"time"

	worker "github.com/contribsys/faktory_worker_go"
)

func registerDefaultJobs(mgr *worker.Manager, rec *recorder) {
	mgr.Register("smoke_job", recordJob(rec))
	mgr.Register("say_hello", recordJob(rec))
}

func registerReminderJobs(mgr *worker.Manager, rec *recorder) {
	mgr.Register("meal_reminder", mealReminder(rec))
}

func recordJob(rec *recorder) worker.Perform {
	return func(ctx context.Context, args ...interface{}) error {
		helper := worker.HelperFor(ctx)
		rec.record(processedJob{
			JID:         helper.Jid(),
			JobType:     helper.JobType(),
			Args:        args,
			ProcessedAt: time.Now().UTC(),
		})
		return nil
	}
}

func mealReminder(rec *recorder) worker.Perform {
	return func(ctx context.Context, args ...interface{}) error {
		helper := worker.HelperFor(ctx)
		select {
		case <-time.After(10 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		rec.record(processedJob{
			JID:         helper.Jid(),
			JobType:     helper.JobType(),
			Args:        args,
			ProcessedAt: time.Now().UTC(),
		})
		return nil
	}
}
