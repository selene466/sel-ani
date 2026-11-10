package anidb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a minimal AniDB UDP API: ENCRYPT, AUTH, MYLISTADD.
type fakeServer struct {
	t      *testing.T
	conn   *net.UDPConn
	apiKey string
	salt   string

	mu       sync.Mutex
	got      []string // commands received, decrypted, without tag/s
	sessions map[string]bool
	mylist   map[string]int    // "aid/epno" -> lid
	state    map[string]string // "aid/epno" -> storage state
	viewed   map[string]string // "aid/epno" -> viewdate
	nextLid  int
	reply    map[string]string // forced reply per command, e.g. "MYLISTADD": "555 BANNED\nflood"
	silent   bool              // drop everything
	lose     map[string]int    // process the next n of a command but drop the reply
	addr     string
}

func newFakeServer(t *testing.T, apiKey string) *fakeServer {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{t: t, conn: conn, apiKey: apiKey, salt: "s4lt", sessions: map[string]bool{},
		mylist: map[string]int{}, state: map[string]string{}, viewed: map[string]string{}, nextLid: 100, reply: map[string]string{}, lose: map[string]int{}, addr: conn.LocalAddr().String()}
	go s.serve()
	t.Cleanup(func() { conn.Close() })
	return s
}

// with changes server settings safely while it runs.
func (s *fakeServer) with(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

func (s *fakeServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.got...)
}

func (s *fakeServer) serve() {
	buf := make([]byte, 2048)
	keys := map[string][]byte{} // client addr -> key
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		data := append([]byte{}, buf[:n]...)
		key := keys[from.String()]
		if key != nil {
			if p, err := decrypt(key, data); err == nil {
				data = p
			}
		}
		cmd, query, _ := strings.Cut(string(data), " ")
		params, _ := url.ParseQuery(strings.ReplaceAll(query, "&amp;", "%26"))
		tag := params.Get("tag")
		s.mu.Lock()
		clean := url.Values{}
		for k, v := range params {
			if k != "tag" && k != "s" {
				clean[k] = v
			}
		}
		s.got = append(s.got, cmd+" "+clean.Encode())
		silent := s.silent
		forced, isForced := s.reply[cmd]
		s.mu.Unlock()
		if silent {
			continue
		}
		var out string
		switch {
		case isForced:
			out = forced
		case cmd == "ENCRYPT":
			if s.apiKey == "" {
				out = "309 API PASSWORD NOT DEFINED"
				break
			}
			out = "209 " + s.salt + " ENCRYPTION ENABLED"
		case cmd == "AUTH":
			if params.Get("pass") != "p&ss" || params.Get("client") != "selani" {
				out = "500 LOGIN FAILED"
				break
			}
			sess := fmt.Sprintf("S%d", len(s.sessions)+1)
			s.mu.Lock()
			s.sessions[sess] = true
			s.mu.Unlock()
			out = "200 " + sess + " 1.2.3.4:5678 LOGIN ACCEPTED"
		case !s.hasSession(params.Get("s")):
			out = "506 INVALID SESSION"
		case cmd == "MYLISTADD" && params.Get("lid") != "":
			out = "311 MYLIST ENTRY EDITED\n1"
		case cmd == "MYLISTADD" && params.Get("edit") == "1":
			// generic edit by aid/epno, as the real server allows
			k := params.Get("aid") + "/" + params.Get("epno")
			s.mu.Lock()
			if _, ok := s.mylist[k]; !ok {
				out = "411 NO SUCH MYLIST ENTRY"
			} else {
				if v := params.Get("state"); v != "" {
					s.state[k] = v
				}
				if params.Get("viewed") == "1" {
					s.viewed[k] = params.Get("viewdate")
				}
				out = "311 MYLIST ENTRY EDITED\n1"
			}
			s.mu.Unlock()
		case cmd == "MYLISTADD":
			// generic adds answer 210 with the count of entries added,
			// 0 when the episode is listed already (checked live)
			k := params.Get("aid") + "/" + params.Get("epno")
			s.mu.Lock()
			if params.Get("aid") == "999999" {
				out = "330 NO SUCH ANIME"
			} else if _, ok := s.mylist[k]; ok {
				out = "210 MYLIST ENTRY ADDED\n0"
			} else {
				s.nextLid++
				s.mylist[k] = s.nextLid
				s.state[k] = params.Get("state")
				if params.Get("viewed") == "1" {
					s.viewed[k] = params.Get("viewdate")
				}
				out = "210 MYLIST ENTRY ADDED\n1"
			}
			s.mu.Unlock()
		default:
			out = "598 UNKNOWN COMMAND"
		}
		s.mu.Lock()
		lost := s.lose[cmd] > 0
		if lost {
			s.lose[cmd]--
		}
		s.mu.Unlock()
		if lost {
			continue
		}
		resp := []byte(tag + " " + out)
		if cmd == "ENCRYPT" && strings.HasPrefix(out, "209") {
			keys[from.String()] = encKey(s.apiKey, s.salt)
		} else if key != nil {
			resp = encrypt(key, resp)
		}
		s.conn.WriteToUDP(resp, from)
	}
}

func (s *fakeServer) hasSession(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

type memState map[string]string

func (m memState) Get(k string) (string, bool, error) { v, ok := m[k]; return v, ok, nil }
func (m memState) Set(k, v string) error              { m[k] = v; return nil }
func (m memState) Delete(k string) error              { delete(m, k); return nil }

func newClient(srv *fakeServer, st memState, port int) (*UDP, *[]time.Duration) {
	var waits []time.Duration
	u := &UDP{
		Cfg: Config{Server: srv.addr, LocalPort: port, Client: "selani", ClientVersion: 1,
			Username: "user", Password: "p&ss", APIKey: srv.apiKey},
		State:   st,
		Timeout: 500 * time.Millisecond,
		sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
	}
	return u, &waits
}

func freePort(t *testing.T) int {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	return p
}

func TestEncryptionRoundTrip(t *testing.T) {
	key := encKey("apikey", "salt")
	for _, msg := range []string{"", "a", "exactly 16 bytes", strings.Repeat("x", 100)} {
		got, err := decrypt(key, encrypt(key, []byte(msg)))
		if err != nil || string(got) != msg {
			t.Errorf("%q: %q %v", msg, got, err)
		}
	}
	if _, err := decrypt(key, []byte("201 plain reply!")); err == nil {
		t.Error("plaintext decrypted without error")
	}
}

func TestMarkWatchedEncrypted(t *testing.T) {
	srv := newFakeServer(t, "k3y")
	st := memState{}
	u, waits := newClient(srv, st, 0)
	defer u.Close()
	ctx := context.Background()

	if err := u.MarkWatched(ctx, 17617, 1, time.Unix(1_800_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	cmds := srv.commands()
	if len(cmds) != 3 || !strings.HasPrefix(cmds[0], "ENCRYPT") || !strings.HasPrefix(cmds[1], "AUTH") || !strings.HasPrefix(cmds[2], "MYLISTADD") {
		t.Fatalf("commands %q", cmds)
	}
	if !strings.Contains(cmds[2], "aid=17617") || !strings.Contains(cmds[2], "epno=1") || !strings.Contains(cmds[2], "generic=1") ||
		!strings.Contains(cmds[2], "viewed=1") || !strings.Contains(cmds[2], "viewdate=1800000000") || !strings.Contains(cmds[2], "state=4") {
		t.Errorf("MYLISTADD params %q", cmds[2])
	}
	// the password survived escaping and the server only understood it decrypted
	if !strings.Contains(cmds[1], "pass=p%26ss") || !strings.Contains(cmds[1], "clientver=1") || !strings.Contains(cmds[1], "protover=3") {
		t.Errorf("AUTH params %q", cmds[1])
	}
	// every packet after the first waited for the 4s gap
	if len(*waits) < 2 {
		t.Errorf("rate limit not applied, waits %v", *waits)
	}
	for _, w := range (*waits)[1:] {
		if w < 3*time.Second || w > 4*time.Second {
			t.Errorf("gap %v, want ~4s", w)
		}
	}

	// already listed (e.g. by hand, unwatched, stored on HDD): the add
	// answers "0 added", so the entry is edited to watched, state kept
	srv.with(func() { srv.state["17617/1"] = "1"; delete(srv.viewed, "17617/1") })
	if err := u.MarkWatched(ctx, 17617, 1, time.Unix(1_800_000_100, 0)); err != nil {
		t.Fatal(err)
	}
	cmds = srv.commands()
	last := cmds[len(cmds)-1]
	if !strings.Contains(last, "edit=1") || !strings.Contains(last, "aid=17617") || !strings.Contains(last, "epno=1") || strings.Contains(last, "state=") {
		t.Errorf("edit packet %q", last)
	}
	srv.with(func() {
		if srv.state["17617/1"] != "1" || srv.viewed["17617/1"] != "1800000100" {
			t.Errorf("entry state=%q viewed=%q", srv.state["17617/1"], srv.viewed["17617/1"])
		}
	})
	// a server answering 310 with a lid still gets an edit by lid
	srv.with(func() { srv.reply["MYLISTADD"] = "310 FILE ALREADY IN MYLIST\n77|1|2|3" })
	u.MarkWatched(ctx, 17617, 1, time.Unix(1_800_000_000, 0))
	srv.with(func() { delete(srv.reply, "MYLISTADD") })
	cmds = srv.commands()
	if last := cmds[len(cmds)-1]; !strings.Contains(last, "lid=77") || !strings.Contains(last, "edit=1") {
		t.Errorf("edit not by lid: %q", last)
	}
	// unknown anime is permanent
	var pe *PermanentError
	if err := u.MarkWatched(ctx, 999999, 1, time.Now()); !errors.As(err, &pe) || pe.Code != 330 {
		t.Errorf("err = %v", err)
	}
}

func TestSessionReusedAcrossRuns(t *testing.T) {
	srv := newFakeServer(t, "k3y")
	st := memState{}
	port := freePort(t)
	u, _ := newClient(srv, st, port)
	if err := u.MarkWatched(context.Background(), 1, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	u.Close()

	// next run, same port: no ENCRYPT/AUTH, straight to the command
	u2, _ := newClient(srv, st, port)
	defer u2.Close()
	before := len(srv.commands())
	if err := u2.MarkWatched(context.Background(), 1, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	cmds := srv.commands()[before:]
	if len(cmds) != 1 || !strings.HasPrefix(cmds[0], "MYLISTADD") {
		t.Errorf("session not reused: %q", cmds)
	}
}

func TestStaleSessionLogsInAgain(t *testing.T) {
	srv := newFakeServer(t, "")
	st := memState{}
	u, _ := newClient(srv, st, 0)
	defer u.Close()
	if err := u.MarkWatched(context.Background(), 1, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	srv.with(func() { srv.sessions = map[string]bool{} }) // server restarted
	before := len(srv.commands())
	if err := u.MarkWatched(context.Background(), 1, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	cmds := srv.commands()[before:]
	if len(cmds) != 3 || !strings.HasPrefix(cmds[1], "AUTH") {
		t.Errorf("relogin: %q", cmds)
	}
	// without an api key there is no ENCRYPT at all
	for _, c := range srv.commands() {
		if strings.HasPrefix(c, "ENCRYPT") {
			t.Error("ENCRYPT sent without api key")
		}
	}
}

func TestBanStopsAllTraffic(t *testing.T) {
	srv := newFakeServer(t, "")
	srv.with(func() { srv.reply["AUTH"] = "555 BANNED\nflooding" })
	st := memState{}
	u, _ := newClient(srv, st, 0)
	defer u.Close()
	err := u.MarkWatched(context.Background(), 1, 1, time.Now())
	if !errors.Is(err, ErrBanned) {
		t.Fatalf("err = %v", err)
	}
	before := len(srv.commands())
	// even a new process stays quiet
	u2, _ := newClient(srv, st, 0)
	defer u2.Close()
	if err := u2.MarkWatched(context.Background(), 1, 1, time.Now()); !errors.Is(err, ErrBanned) {
		t.Errorf("err = %v", err)
	}
	if len(srv.commands()) != before {
		t.Error("packets sent while banned")
	}
}

func TestFailedLoginIsNotRetried(t *testing.T) {
	srv := newFakeServer(t, "")
	st := memState{}
	u, _ := newClient(srv, st, 0)
	u.Cfg.Password = "wrong"
	defer u.Close()
	var pe *PermanentError
	if err := u.MarkWatched(context.Background(), 1, 1, time.Now()); !errors.As(err, &pe) || pe.Code != 500 {
		t.Fatalf("err = %v", err)
	}
	before := len(srv.commands())
	if err := u.MarkWatched(context.Background(), 1, 1, time.Now()); !errors.Is(err, ErrLoginBlocked) {
		t.Errorf("err = %v", err)
	}
	if len(srv.commands()) != before {
		t.Error("login retried with the same bad credentials")
	}
	// fixing the config unblocks
	u.Cfg.Password = "p&ss"
	if err := u.MarkWatched(context.Background(), 1, 1, time.Now()); err != nil {
		t.Errorf("after fix: %v", err)
	}
}

func TestNoReply(t *testing.T) {
	srv := newFakeServer(t, "")
	srv.with(func() { srv.silent = true })
	u, _ := newClient(srv, memState{}, 0)
	defer u.Close()
	u.Timeout = 100 * time.Millisecond
	if err := u.MarkWatched(context.Background(), 1, 1, time.Now()); !errors.Is(err, ErrNoReply) {
		t.Errorf("err = %v", err)
	}
}

func TestParseReply(t *testing.T) {
	r, tag, ok := parseReply([]byte("t1 310 FILE ALREADY IN MYLIST\n55|a&amp;b<br />c\n"))
	if !ok || tag != "t1" || r.Code != 310 || r.Text != "FILE ALREADY IN MYLIST" || r.Data[0] != "55|a&b\nc" {
		t.Errorf("%+v %q %v", r, tag, ok)
	}
	r, tag, ok = parseReply([]byte("598 UNKNOWN COMMAND"))
	if !ok || tag != "" || r.Code != 598 {
		t.Errorf("untagged: %+v %q %v", r, tag, ok)
	}
	if _, _, ok := parseReply([]byte("garbage")); ok {
		t.Error("garbage parsed")
	}
}

func TestMyListProgress(t *testing.T) {
	srv := newFakeServer(t, "k3y")
	u, _ := newClient(srv, memState{}, 0)
	defer u.Close()
	ctx := context.Background()
	for _, c := range []struct {
		reply          string
		watched, total int
		fail           bool
	}{
		{"312 MULTIPLE MYLIST ENTRIES\nFrieren|28|0|5|0|0|1-3,5,S1|x", 4, 28, false},
		{"221 MYLIST\n1|2|3|4|5|1700000000|1|1700000500|0|0||0", 1, 0, false},
		{"221 MYLIST\n1|2|3|4|5|1700000000|1|0|0|0||0", 0, 0, false},
		{"411 NO SUCH MYLIST ENTRY", 0, 0, false},
		{"321 NO SUCH ENTRY", 0, 0, false},
		{"312 MULTIPLE MYLIST ENTRIES\nFrieren|28|garbage", 0, 0, true},
		{"330 NO SUCH ANIME", 0, 0, true},
	} {
		srv.with(func() { srv.reply["MYLIST"] = c.reply })
		w, tot, err := u.MyListProgress(ctx, 17617)
		if (err != nil) != c.fail || w != c.watched || tot != c.total {
			t.Errorf("%q: %d %d %v", c.reply, w, tot, err)
		}
	}
}

func TestCountEpisodes(t *testing.T) {
	for in, want := range map[string]int{"": 0, "1": 1, "1-3": 3, "1-3,5,7-8": 6, "S1,S2": 0, "1,S3": 1} {
		if n, ok := countEpisodes(in); !ok || n != want {
			t.Errorf("%q: %d %v", in, n, ok)
		}
	}
	for _, in := range []string{"x", "3-1", "1,,2", "1-"} {
		if _, ok := countEpisodes(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}
