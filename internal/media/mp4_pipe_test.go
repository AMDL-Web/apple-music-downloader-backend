package media

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"amdl/internal/config"
)

func writePipeTestCommand(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pipe lifecycle test uses a POSIX helper script")
	}
	path := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newPipeTestProcessor(t *testing.T, command string) *MP4Processor {
	t.Helper()
	cfg := config.Config{}
	cfg.Download.TempDir = t.TempDir()
	cfg.Tools.FFmpeg = command
	return newMP4Processor(cfg)
}

func TestProduceToFlatFilePreservesProducerAndFFmpegFailures(t *testing.T) {
	command := writePipeTestCommand(t, "cat >/dev/null\nexit 23")
	p := newPipeTestProcessor(t, command)
	producerErr := errors.New("forced producer failure")
	var callbackCalls atomic.Int32

	err := p.produceToFlatFile(context.Background(), filepath.Join(t.TempDir(), "out.m4a"), func(w io.Writer) error {
		if _, err := io.WriteString(w, "partial fragmented mp4"); err != nil {
			return err
		}
		return producerErr
	}, func() { callbackCalls.Add(1) })
	if !errors.Is(err, producerErr) {
		t.Fatalf("pipeline error = %v, want producer error in chain", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("pipeline error = %v, want ffmpeg exit status 23 in chain", err)
	}
	if got := callbackCalls.Load(); got != 0 {
		t.Fatalf("afterInput called %d times after producer failure, want 0", got)
	}
}

func TestProduceToFlatFileCallbackPrecedesWaitAndCancellationReapsChild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "waiting")
	// The marker is written only after stdin reaches EOF. exec replaces the
	// shell so CommandContext kills the actual sleeper and cmd.Wait can reap it
	// without leaving a descendant holding the stderr pipe open.
	command := writePipeTestCommand(t, "cat >/dev/null\nprintf ready > \""+marker+"\"\nexec sleep 30")
	p := newPipeTestProcessor(t, command)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callback := make(chan struct{}, 1)
	done := make(chan error, 1)
	outPath := filepath.Join(t.TempDir(), "out.m4a")

	go func() {
		done <- p.produceToFlatFile(ctx, outPath, func(w io.Writer) error {
			_, err := io.WriteString(w, strings.Repeat("fragment", 1024))
			return err
		}, func() { callback <- struct{}{} })
	}()

	select {
	case <-callback:
	case err := <-done:
		t.Fatalf("pipeline returned before afterInput callback: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("afterInput callback was not published after stdin close")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake ffmpeg did not enter its post-input wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("pipeline returned before ffmpeg flush/wait completed: %v", err)
	default:
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled pipeline error = %v, want context.Canceled in chain", err)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("canceled pipeline error = %v, want reaped process error in chain", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled pipeline did not reap ffmpeg promptly")
	}
}

func TestProduceToFlatFileEarlyExitUnblocksProducer(t *testing.T) {
	command := writePipeTestCommand(t, "exit 17")
	p := newPipeTestProcessor(t, command)
	done := make(chan error, 1)
	outPath := filepath.Join(t.TempDir(), "out.m4a")

	go func() {
		done <- p.produceToFlatFile(context.Background(), outPath, func(w io.Writer) error {
			// More than a pipe buffer ensures a child that consumes nothing must
			// interrupt this write with EPIPE rather than let production finish.
			_, err := io.CopyN(w, zeroReader{}, 8<<20)
			return err
		}, nil)
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "exit status 17") {
			t.Fatalf("early-exit pipeline error = %v, want exit status 17", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("producer remained blocked after ffmpeg exited")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
