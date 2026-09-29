package task

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRunTracksTasks(t *testing.T) {
	var r Registry

	done, report, err := r.Run(context.Background(), "count files", func(ctx context.Context, t Task) (string, int, error) {
		if t.ID != "t1" || t.Status != StatusRunning {
			return "", 0, errors.New("the runner was handed a task that had not started: " + t.ID)
		}
		return "there are 12", 340, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if report != "there are 12" || done.Status != StatusDone || done.Tokens != 340 {
		t.Fatalf("the result was not recorded: %+v", done)
	}

	// A failed task keeps its report slot empty but records why.
	boom := errors.New("boom")
	failed, _, err := r.Run(context.Background(), "", func(ctx context.Context, t Task) (string, int, error) {
		return "", 0, boom
	})
	if !errors.Is(err, boom) || failed.Status != StatusFailed || failed.Err != boom {
		t.Fatalf("a failure was not recorded: %+v", failed)
	}
	if failed.ID != "t2" || failed.Description != "task" {
		t.Fatalf("ids should count up and an empty description gets a default: %+v", failed)
	}

	// The snapshot is a copy: the UI reads it while tasks are still running.
	list := r.Snapshot()
	if len(list) != 2 {
		t.Fatalf("both tasks should be listed: %+v", list)
	}
	list[0].Description = "mutated"
	if r.Snapshot()[0].Description == "mutated" {
		t.Fatal("Snapshot handed out the registry's own tasks")
	}
}

// Ten background tasks are not ten simultaneous requests: a key that allows
// two at a time gets two, and the rest wait their turn rather than being
// refused by the provider.
func TestRegistryLimitsWhatRunsAtOnce(t *testing.T) {
	var r Registry
	var mu sync.Mutex
	var running, peak int

	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < MaxRunning+3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Run(context.Background(), "job", func(ctx context.Context, task Task) (string, int, error) {
				mu.Lock()
				running++
				if running > peak {
					peak = running
				}
				mu.Unlock()

				<-release

				mu.Lock()
				running--
				mu.Unlock()
				return "done", 1, nil
			})
		}()
	}

	// Give the runners time to pile up, then check what the queue let through.
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return running == MaxRunning })
	if queued := countStatus(&r, StatusQueued); queued != 3 {
		t.Fatalf("the rest must wait in the queue, got %d", queued)
	}

	close(release)
	wg.Wait()
	if peak > MaxRunning {
		t.Fatalf("%d tasks ran at once, the limit is %d", peak, MaxRunning)
	}
	if done := countStatus(&r, StatusDone); done != MaxRunning+3 {
		t.Fatalf("every task must still finish, %d did", done)
	}
}

// A task cancelled while it waits has to end somewhere, or it sits in the list
// as queued for the rest of the session.
func TestQueuedTaskEndsWhenCancelled(t *testing.T) {
	var r Registry
	hold := make(chan struct{})
	defer close(hold)

	for i := 0; i < MaxRunning; i++ {
		go r.Run(context.Background(), "hog", func(ctx context.Context, task Task) (string, int, error) {
			<-hold
			return "", 0, nil
		})
	}
	waitFor(t, func() bool { return countStatus(&r, StatusRunning) == MaxRunning })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, _, err := r.Run(ctx, "never runs", func(context.Context, Task) (string, int, error) {
		t.Error("a cancelled task must not run")
		return "", 0, nil
	})
	if err == nil {
		t.Fatal("cancelling while queued must be reported")
	}
	if failed := countStatus(&r, StatusFailed); failed != 1 {
		t.Fatalf("the cancelled task must be recorded as ended, got %d failed", failed)
	}
	// And it took no time, because it never began. This read time.Since(zero)
	// and reported 2562047h47m16s — a Duration at its ceiling, in a column
	// meant for seconds.
	if cancelled.Elapsed != 0 {
		t.Errorf("a task that never started spent no time working, got %s", cancelled.Elapsed)
	}
}

// The tail kept of a task's output is cut by byte, and a character is not one.
// Landing inside a multi-byte rune left a broken glyph at the head of every
// trimmed output — and a task's output is where non-English text is most
// likely to be.
func TestTrimmedOutputIsStillValidText(t *testing.T) {
	var r Registry
	r.start("a task")

	// Three bytes per character, so a cut at a byte boundary lands inside one
	// two times out of three whatever the length.
	for i := 0; i < 400; i++ {
		r.Progress("t1", strings.Repeat("日", 10))
	}

	got := find(&r, "t1").Output
	if len(got) <= maxOutput {
		t.Fatalf("the output was never trimmed, so the test proves nothing: %d bytes", len(got))
	}
	if !utf8.ValidString(got) {
		t.Error("the trimmed output is not valid text")
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Error("the trim left a replacement glyph behind")
	}
}

func countStatus(r *Registry, want Status) int {
	n := 0
	for _, t := range r.Snapshot() {
		if t.Status == want {
			n++
		}
	}
	return n
}

// waitFor polls rather than synchronises, so its budget has to survive a busy
// machine: these tests start goroutines and wait for the scheduler to run them,
// and a laptop with six test binaries on it can take a while to get round to
// them. Five seconds is nothing when the answer arrives in milliseconds, which
// it does, and the difference between a failure that means something and one
// that means the machine was busy.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the tasks to settle")
}

// A task can be watched while it works, and stopped — queued or running,
// since a queue is exactly where stopping something is most useful.
func TestProgressAndStop(t *testing.T) {
	var r Registry
	started, blocked := make(chan struct{}), make(chan struct{})

	go r.Run(context.Background(), "slow", func(ctx context.Context, self Task) (string, int, error) {
		r.Progress(self.ID, "line one\n")
		r.Progress(self.ID, "line two\n")
		close(started)
		<-ctx.Done() // stopped from outside
		close(blocked)
		return "gave up", 0, ctx.Err()
	})
	<-started

	if got := find(&r, "t1").Output; got != "line one\nline two\n" {
		t.Fatalf("a task's output must be readable while it works, got %q", got)
	}
	if !r.Stop("t1") {
		t.Fatal("a running task must be stoppable")
	}
	<-blocked

	waitFor(t, func() bool { return find(&r, "t1").Status == StatusFailed })
	if r.Stop("t1") {
		t.Error("a task that has ended is not there to stop")
	}
	if r.Stop("t99") {
		t.Error("a task that never existed is not there to stop either")
	}
}

func find(r *Registry, id string) Task {
	for _, t := range r.Snapshot() {
		if t.ID == id {
			return t
		}
	}
	return Task{}
}

// Work somebody is waiting on does not queue: two long background tasks must
// not stall the conversation that started them.
func TestForegroundWorkSkipsTheQueue(t *testing.T) {
	var r Registry
	hold := make(chan struct{})
	defer close(hold)

	for i := 0; i < MaxRunning; i++ {
		go r.Run(context.Background(), "background", func(ctx context.Context, _ Task) (string, int, error) {
			<-hold
			return "", 0, nil
		})
	}
	waitFor(t, func() bool { return countStatus(&r, StatusRunning) == MaxRunning })

	done := make(chan string, 1)
	go func() {
		_, report, _ := r.RunNow(context.Background(), "foreground", func(ctx context.Context, _ Task) (string, int, error) {
			return "answered", 1, nil
		})
		done <- report
	}()

	select {
	case report := <-done:
		if report != "answered" {
			t.Fatalf("report = %q", report)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("foreground work waited for the queue")
	}
}
