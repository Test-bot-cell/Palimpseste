package media

// The asynchronous work queue (§10.1): the upload handler enqueues and returns
// immediately — heavy encoding never blocks the editor. One worker goroutine
// per queue keeps wazero's compiled module warm and serialises disk writes;
// progress and completion stream to the caller's event sink (the edit server's
// SSE hub).

import (
	"bytes"
	"context"
	"sync"
)

// Job is one queued ingest.
type Job struct {
	ID       string // caller-chosen correlation id, echoed in every event
	Filename string
	Bytes    []byte
}

// Event is one progress notification. Exactly one of Stage, Result or Err is
// meaningful: a stage label while working, the result on success, the error on
// failure.
type Event struct {
	ID     string  `json:"id"`
	Stage  string  `json:"stage,omitempty"`
	Result *Result `json:"result,omitempty"`
	Err    string  `json:"error,omitempty"`
}

// Queue is the per-site ingest queue.
type Queue struct {
	siteDir string
	sink    func(Event)

	mu      sync.Mutex
	pending chan Job
	started bool
	wg      sync.WaitGroup
}

// NewQueue builds a queue writing under siteDir and reporting through sink
// (which must be safe for concurrent use; the edit server's hub is).
func NewQueue(siteDir string, sink func(Event)) *Queue {
	return &Queue{siteDir: siteDir, sink: sink, pending: make(chan Job, 16)}
}

// Start launches the worker; it drains until ctx is cancelled.
func (q *Queue) Start(ctx context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started {
		return
	}
	q.started = true
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-q.pending:
				q.run(job)
			}
		}
	}()
}

// Enqueue queues one job; it reports false when the queue is saturated (the
// caller answers 503 rather than blocking the editor — §10.1's promise is
// "never blocks", not "infinite memory").
func (q *Queue) Enqueue(job Job) bool {
	select {
	case q.pending <- job:
		return true
	default:
		return false
	}
}

// Wait blocks until the worker goroutine has exited (after ctx cancellation);
// tests use it, the CLI relies on process exit.
func (q *Queue) Wait() { q.wg.Wait() }

func (q *Queue) run(job Job) {
	res, err := Ingest(q.siteDir, job.Filename, bytes.NewReader(job.Bytes), func(stage string) {
		q.sink(Event{ID: job.ID, Stage: stage})
	})
	if err != nil {
		q.sink(Event{ID: job.ID, Err: err.Error()})
		return
	}
	q.sink(Event{ID: job.ID, Result: res})
}
