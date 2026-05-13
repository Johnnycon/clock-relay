package main

import (
	"sync"
	"time"
)

type processedJob struct {
	JID         string    `json:"jid"`
	JobType     string    `json:"job_type"`
	Args        []any     `json:"args"`
	ProcessedAt time.Time `json:"processed_at"`
}

type recorder struct {
	mu   sync.RWMutex
	jobs map[string]processedJob
}

func newRecorder() *recorder {
	return &recorder{jobs: map[string]processedJob{}}
}

func (r *recorder) record(job processedJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[job.JID] = job
}

func (r *recorder) get(jid string) (processedJob, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	job, ok := r.jobs[jid]
	return job, ok
}

func (r *recorder) list() []processedJob {
	r.mu.RLock()
	defer r.mu.RUnlock()
	jobs := make([]processedJob, 0, len(r.jobs))
	for _, job := range r.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}
