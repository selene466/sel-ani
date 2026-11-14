package player

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sel-ani/internal/domain"
)

func TestRemoteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	script := filepath.Join(dir, "p.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, addr, &Exec{Command: script, NoDetach: true}, func(string) {})
	time.Sleep(100 * time.Millisecond)

	r := &Remote{Addr: addr}
	m := domain.Media{Title: "t", Video: "https://x/v.m3u8", Stream: domain.Stream{Referer: "https://r/"}}
	if err := r.Play(ctx, m); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "https://x/v.m3u8") {
		t.Fatalf("player got %q", b)
	}
	m.Video = "--script=/tmp/evil.lua"
	if err := r.Play(ctx, m); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("err = %v", err)
	}
}
