//go:build unix

package player

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// longPlayer writes a player script that never exits on its own.
func longPlayer(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "player")
	os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700)
	return p
}

func gone(pid int) bool {
	// reaped processes no longer exist; zombies would still answer signal 0
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

func TestExecStopKillsDetachedPlayer(t *testing.T) {
	p := &Exec{Command: longPlayer(t, "exec sleep 30")}
	if err := p.Play(context.Background(), media); err != nil {
		t.Fatal(err)
	}
	pid := p.running.cmd.Process.Pid
	start := time.Now()
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second || !gone(pid) {
		t.Errorf("player not stopped (took %v)", time.Since(start))
	}
	if err := p.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestExecStopEscalatesToKill(t *testing.T) {
	// ignores SIGTERM, and so do its children
	p := &Exec{Command: longPlayer(t, "trap '' TERM\nwhile :; do sleep 1; done"), killGrace: 200 * time.Millisecond}
	if err := p.Play(context.Background(), media); err != nil {
		t.Fatal(err)
	}
	pid := p.running.cmd.Process.Pid
	time.Sleep(100 * time.Millisecond) // let the trap install
	start := time.Now()
	p.Stop()
	if d := time.Since(start); d > 2*time.Second || !gone(pid) {
		t.Errorf("stubborn player survived or Stop took %v", d)
	}
}

func TestExecWaitRespectsContext(t *testing.T) {
	p := &Exec{Command: longPlayer(t, "exec sleep 30")}
	if err := p.Play(context.Background(), media); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := p.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("Wait = %v after %v", err, time.Since(start))
	}
	// Ctrl+C on sel-ani must not take the player down: still running
	if gone(p.running.cmd.Process.Pid) {
		t.Error("player died when Wait was cancelled")
	}
}
