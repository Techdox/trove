package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunSupervisedRestartsPanickingWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := newWorkerMonitor("test")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var attempts atomic.Int32
	running := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSupervisedWithBackoff(ctx, logger, "test", monitor, time.Millisecond, 2*time.Millisecond, func() {
			if attempts.Add(1) < 3 {
				panic("test panic")
			}
			close(running)
			<-ctx.Done()
		})
	}()

	select {
	case <-running:
	case <-time.After(time.Second):
		t.Fatal("worker was not restarted after panic")
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("worker attempts = %d, want 3", got)
	}
	if err := monitor.health(); err != nil {
		t.Fatalf("worker health after restart = %v", err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop after context cancellation")
	}
}

func TestRunSupervisedMarksWorkerUnavailableDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	monitor := newWorkerMonitor("freshness")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	started := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		runSupervisedWithBackoff(ctx, logger, "freshness", monitor, time.Hour, time.Hour, func() {
			close(started)
			panic("test panic")
		})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	deadline := time.Now().Add(time.Second)
	for {
		err := monitor.health()
		if err != nil {
			if !strings.Contains(err.Error(), "freshness") {
				t.Fatalf("worker health error = %q, want worker name", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker was not marked unavailable during backoff")
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not leave backoff after context cancellation")
	}
}
