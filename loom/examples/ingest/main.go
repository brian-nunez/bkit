// Command ingest is a complete service built on loom: a video ingest worker
// that runs one graph per job on a single shared, elastic pool.
//
//	playlist -> download -> merge ----\
//	                    \-> thumbnail -> upload -> notify
//	cleanup runs after everything, whatever happened (AllDone)
//
// The side effects are simulated so the program runs anywhere. It feeds a
// batch of jobs, scales the pool with queue depth, sheds workers with
// Interrupt halfway through, resumes failed jobs, and shuts down when the
// batch is done or on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/brian-nunez/bkit/loom"
)

// ---- the application's own types and side effects (simulated) -------------

type Job struct {
	ID          string
	PlaylistURL string
	Webhook     string // optional
}

type Playlist struct{ Segments []string }

type Files struct{ Paths []string }

type Video struct{ Path string }

type Thumb struct{ Path string }

var errBadInput = errors.New("ingest: bad input")

// flakyUploads makes the first upload of some jobs fail, to show Resume.
var flakyUploads sync.Map

func work(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(d + rand.N(d)):
		return nil
	}
}

func fetchPlaylist(ctx context.Context, url string) (Playlist, error) {
	if url == "" {
		return Playlist{}, errBadInput
	}
	return Playlist{Segments: []string{"0.ts", "1.ts", "2.ts"}}, work(ctx, 10*time.Millisecond)
}

func downloadAll(ctx context.Context, segments []string) (Files, error) {
	return Files{Paths: segments}, work(ctx, 60*time.Millisecond)
}

func mergeFiles(ctx context.Context, f Files) (Video, error) {
	return Video{Path: fmt.Sprintf("video-%d.mp4", len(f.Paths))}, work(ctx, 30*time.Millisecond)
}

func makeThumb(ctx context.Context, _ Files) (Thumb, error) {
	return Thumb{Path: "thumb.jpg"}, work(ctx, 20*time.Millisecond)
}

func uploadVideo(ctx context.Context, jobID string, v Video, _ Thumb) (string, error) {
	if _, loaded := flakyUploads.LoadOrStore(jobID, true); !loaded && jobID == "job-3" {
		return "", errors.New("upload: expired credentials")
	}
	return "s3://videos/" + jobID + "/" + v.Path, work(ctx, 20*time.Millisecond)
}

func postWebhook(ctx context.Context, _, _ string) error { return work(ctx, 5*time.Millisecond) }

func removeAll([]string) {}

// ---- policy: owned by the application, not loom ----------------------------

// retry is this service's policy. loom only reports what happened.
func retry(a loom.Attempt) (time.Duration, bool) {
	switch {
	case errors.Is(a.Err, loom.ErrInterrupted):
		return 0, true // a worker was scaled down: run again right away
	case errors.Is(a.Err, errBadInput):
		return 0, false // retrying cannot fix the input
	default:
		return loom.Backoff(2, 10*time.Millisecond, 100*time.Millisecond)(a)
	}
}

// ---- graph definition -------------------------------------------------------

// ingest holds one job's graph and the handles the caller reads after a run.
type ingest struct {
	graph  *loom.Graph
	upload *loom.Task[string]
}

// newIngest builds one job's graph. The job reaches the tasks through
// closures, so each job gets its own graph. Building does no work.
func newIngest(job Job, pool *loom.Pool, hooks loom.Hooks) ingest {
	g := loom.New(
		loom.WithName("ingest"),
		loom.WithPool(pool),
		loom.WithHooks(hooks),
		loom.WithRetry(retry),
	)

	playlist := loom.Add(g, "playlist", func(ctx context.Context, _ *loom.Inputs) (Playlist, error) {
		return fetchPlaylist(ctx, job.PlaylistURL)
	}, loom.WithTimeout(5*time.Second))

	// How download fans out over segments is this task's business.
	download := loom.Add(g, "download", func(ctx context.Context, in *loom.Inputs) (Files, error) {
		return downloadAll(ctx, loom.Get(in, playlist).Segments)
	}, loom.After(playlist), loom.WithTimeout(time.Minute))

	// merge and thumbnail run in parallel once download is done.
	merge := loom.Add(g, "merge", func(ctx context.Context, in *loom.Inputs) (Video, error) {
		return mergeFiles(ctx, loom.Get(in, download))
	}, loom.After(download))

	thumb := loom.Add(g, "thumbnail", func(ctx context.Context, in *loom.Inputs) (Thumb, error) {
		return makeThumb(ctx, loom.Get(in, download))
	}, loom.After(download))

	upload := loom.Add(g, "upload", func(ctx context.Context, in *loom.Inputs) (string, error) {
		return uploadVideo(ctx, job.ID, loom.Get(in, merge), loom.Get(in, thumb))
	}, loom.After(merge, thumb), loom.WithRetry(nil)) // a failed upload waits for a job-level Resume

	notify := loom.Add(g, "notify", func(ctx context.Context, in *loom.Inputs) (struct{}, error) {
		if job.Webhook == "" {
			return struct{}{}, loom.ErrSkip
		}
		return struct{}{}, postWebhook(ctx, job.Webhook, loom.Get(in, upload))
	}, loom.After(upload), loom.WithRetry(nil))

	// Always clean up local files, whatever happened upstream.
	loom.Add(g, "cleanup", func(_ context.Context, in *loom.Inputs) (struct{}, error) {
		if files, ok := loom.Lookup(in, download); ok {
			removeAll(files.Paths)
		}
		return struct{}{}, nil
	}, loom.After(download, merge, thumb, upload, notify), loom.WithTrigger(loom.AllDone))

	return ingest{graph: g, upload: upload}
}

// ---- one job ----------------------------------------------------------------

func handle(ctx context.Context, job Job, pool *loom.Pool, log *slog.Logger) {
	hooks := loom.Hooks{
		// Graph hooks close over the job, so events from many concurrent runs
		// on the shared pool stay attributable.
		OnTaskRetry: func(_ context.Context, e loom.TaskEvent) {
			log.Warn("retry", "job", job.ID, "task", e.Task, "attempt", e.Attempt, "err", e.Err)
		},
	}
	in := newIngest(job, pool, hooks)

	res, err := in.graph.Run(ctx)

	// Job-level reruns. Resume skips everything that already succeeded, so a
	// failed upload does not repeat the download.
	for i := 0; err != nil && i < 2 && ctx.Err() == nil; i++ {
		if errors.Is(err, errBadInput) {
			break
		}
		log.Info("resuming job", "job", job.ID, "upload", res.State(in.upload))
		res, err = in.graph.Run(ctx, loom.Resume(res))
	}

	if err != nil {
		var e *loom.Error
		if errors.As(err, &e) {
			log.Error("job failed", "job", job.ID, "task", e.Task, "attempt", e.Attempt, "err", e.Err)
		}
		return
	}
	log.Info("job done", "job", job.ID, "location", loom.Get(res, in.upload))
}

// ---- service ----------------------------------------------------------------

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Pool-wide hooks: worker lifecycle and task counts for every graph.
	var finished atomic.Int64
	pool, err := loom.NewPool(loom.WithWorkers(2), loom.WithHooks(loom.Hooks{
		OnResize: func(e loom.ResizeEvent) { log.Info("pool resized", "from", e.From, "to", e.To) },
		OnWorkerStop: func(e loom.WorkerEvent) {
			if e.Interrupted {
				log.Info("worker interrupted", "worker", e.Worker)
			}
		},
		OnTaskFinish: func(context.Context, loom.TaskEvent) { finished.Add(1) },
	}))
	if err != nil {
		log.Error("pool", "err", err)
		os.Exit(1)
	}

	jobs := make(chan Job)
	var depth atomic.Int64
	go func() {
		defer close(jobs)
		for i := 1; i <= 12; i++ {
			job := Job{ID: fmt.Sprintf("job-%d", i), PlaylistURL: "https://cdn.example/" + fmt.Sprint(i)}
			if i == 5 {
				job.PlaylistURL = "" // bad input: fails without retries
			}
			if i%2 == 0 {
				job.Webhook = "https://hooks.example/done"
			}
			depth.Add(1)
			select {
			case jobs <- job:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Autoscale on queue depth: grow fast, shrink gracefully.
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				switch d, size := int(depth.Load()), pool.Size(); {
				case d > size && size < 8:
					_ = pool.Grow(min(d-size, 8-size))
				case d < size/2 && size > 2:
					_ = pool.Shrink(size / 4) // workers finish their current task, then leave
				}
			}
		}
	}()

	// Shed half the workers once, immediately. Their tasks get
	// ErrInterrupted, and the retry policy above runs them again.
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(150 * time.Millisecond):
			if n := pool.Size() / 2; n > 0 {
				log.Info("shedding workers", "count", n)
				_ = pool.Shrink(n, loom.Interrupt())
			}
		}
	}()

	var wg sync.WaitGroup
	for job := range jobs {
		wg.Go(func() {
			defer depth.Add(-1)
			handle(ctx, job, pool, log)
		})
	}
	wg.Wait()

	// Drain: running tasks get 5s to finish, then they are interrupted.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown", "err", err)
	}
	log.Info("done", "tasks finished", finished.Load())
}
