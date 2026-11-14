package player

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"sel-ani/internal/domain"
)

// Remote sends the stream to a Serve listener, normally on the other end of
// an ssh -R tunnel, so the player runs on the ssh client.
type Remote struct{ Addr string }

func (r *Remote) Interactive() bool          { return true }
func (r *Remote) Wait(context.Context) error { return nil }
func (r *Remote) Stop() error                { return nil }

func (r *Remote) Play(ctx context.Context, m domain.Media) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", r.Addr)
	if err != nil {
		return fmt.Errorf("no listener at %s (run sel-ani --listen on the client, ssh -R): %w", r.Addr, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(c).Encode(m); err != nil {
		return err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if line = strings.TrimSpace(line); line != "ok" {
		return errors.New("client: " + line)
	}
	return nil
}

// Serve plays every Media received on addr with p until ctx is done.
func Serve(ctx context.Context, addr string, p *Exec, logf func(string)) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		serveConn(ctx, c, p, logf)
	}
}

func serveConn(ctx context.Context, c net.Conn, p *Exec, logf func(string)) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	var m domain.Media
	err := json.NewDecoder(bufio.NewReader(c)).Decode(&m)
	if err == nil {
		err = checkMedia(m)
	}
	if err == nil {
		logf("playing " + m.Title)
		_ = p.Stop()
		err = p.Play(ctx, m)
	}
	if err != nil {
		logf("error: " + err.Error())
		fmt.Fprintln(c, strings.ReplaceAll(err.Error(), "\n", " "))
		return
	}
	fmt.Fprintln(c, "ok")
}

// checkMedia keeps a stray local connection from smuggling player options:
// the URLs end up in argv.
func checkMedia(m domain.Media) error {
	for _, u := range []string{m.Video, m.Stream.Audio, m.Stream.Subtitle} {
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return errors.New("refusing non-http url")
		}
	}
	if m.Video == "" {
		return errors.New("no video url")
	}
	return nil
}
