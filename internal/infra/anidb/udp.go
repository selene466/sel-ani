package anidb

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UDP API reply codes used here.
const (
	codeLoginAccepted     = 200
	codeLoginAcceptedNew  = 201 // a newer client version exists
	codeEncryptionEnabled = 209
	codeMylistAdded       = 210
	codeMylistExists      = 310
	codeMylistEdited      = 311
	codeMylist            = 221
	codeMylistMultiple    = 312
	codeNoSuchMylistEntry = 321 // as MYLIST answers for an anime not listed
	codeNoSuchFile        = 320
	codeNoSuchAnime       = 330
	codeNoSuchGroup       = 350
	codeNoSuchEntry       = 411
	codeLoginFailed       = 500
	codeLoginFirst        = 501
	codeClientOutdated    = 503
	codeClientBanned      = 504
	codeIllegalInput      = 505
	codeInvalidSession    = 506
	codeBanned            = 555
	codeUnknownCommand    = 598
)

// Persisted state keys.
const (
	kvLastSend     = "anidb_last_send"     // unix nanoseconds of the last packet
	kvSession      = "anidb_session"       // session key
	kvSalt         = "anidb_salt"          // encryption salt of the session
	kvSessionUsed  = "anidb_session_used"  // unix seconds of last use
	kvSessionPort  = "anidb_session_port"  // local port the session belongs to
	kvSessionAuth  = "anidb_session_auth"  // credentials fingerprint of the session
	kvBannedUntil  = "anidb_banned_until"  // unix seconds
	kvLoginBlocked = "anidb_login_blocked" // fingerprint|reason after a refused login
)

// sessionIdle: AniDB drops idle sessions after about 35 minutes; stay under.
const sessionIdle = 30 * time.Minute

// banPause: how long to stay silent after a ban reply.
const banPause = 35 * time.Minute

// Config holds the AniDB account and client registration.
type Config struct {
	Server        string // host:port, e.g. api.anidb.net:9000
	LocalPort     int    // fixed local port, 0 = any (no session reuse across runs)
	Client        string // registered client name
	ClientVersion int
	Username      string
	Password      string
	APIKey        string // enables encryption
}

// State persists rate limit and session data across runs (store.DB).
type State interface {
	Get(key string) (string, bool, error)
	Set(key, value string) error
	Delete(key string) error
}

// Error kinds the tracker acts on.
var (
	ErrBanned       = errors.New("AniDB: banned, pausing")
	ErrLoginBlocked = errors.New("AniDB: login refused earlier; fix [anidb] in the config")
	ErrNoReply      = errors.New("AniDB: no reply")
)

// PermanentError is a reply that will not change when retried.
type PermanentError struct {
	Code int
	Text string
}

func (e *PermanentError) Error() string { return fmt.Sprintf("AniDB: %d %s", e.Code, e.Text) }

// Reply is a parsed UDP API reply.
type Reply struct {
	Code int
	Text string   // rest of the first line
	Data []string // following lines
}

// UDP is an AniDB UDP API client. Calls are serialized.
type UDP struct {
	callMu sync.Mutex

	Cfg     Config
	State   State
	Gap     time.Duration // minimum time between packets; 0 = 4s
	Timeout time.Duration // reply wait; 0 = 30s

	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	conn    *net.UDPConn
	port    int
	session string
	key     []byte // AES-128 key while encryption is on
	seq     int
	tagBase string
}

func (u *UDP) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

func (u *UDP) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if u.sleep != nil {
		return u.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (u *UDP) gap() time.Duration {
	if u.Gap > 0 {
		return u.Gap
	}
	return 4 * time.Second
}

func (u *UDP) timeout() time.Duration {
	if u.Timeout > 0 {
		return u.Timeout
	}
	// AniDB can take well over 15s to answer under load
	return 30 * time.Second
}

// fingerprint identifies the credentials without storing the password.
func (u *UDP) fingerprint() string {
	h := sha256.Sum256([]byte(strings.Join([]string{u.Cfg.Username, u.Cfg.Password, u.Cfg.APIKey, u.Cfg.Client, strconv.Itoa(u.Cfg.ClientVersion)}, "\x00")))
	return hex.EncodeToString(h[:8])
}

func (u *UDP) kvInt(key string) int64 {
	v, ok, _ := u.State.Get(key)
	if !ok {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// Close closes the socket. The session stays valid for the next run.
func (u *UDP) Close() error {
	if u.conn == nil {
		return nil
	}
	err := u.conn.Close()
	u.conn = nil
	return err
}

func (u *UDP) dial() error {
	if u.conn != nil {
		return nil
	}
	raddr, err := net.ResolveUDPAddr("udp", u.Cfg.Server)
	if err != nil {
		return fmt.Errorf("AniDB server %q: %w", u.Cfg.Server, err)
	}
	var laddr *net.UDPAddr
	if u.Cfg.LocalPort > 0 {
		laddr = &net.UDPAddr{Port: u.Cfg.LocalPort}
	}
	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil && laddr != nil {
		// port taken, e.g. a second sel-ani: work, but without session reuse
		conn, err = net.DialUDP("udp", nil, raddr)
	}
	if err != nil {
		return fmt.Errorf("AniDB: %w", err)
	}
	u.conn = conn
	u.port = conn.LocalAddr().(*net.UDPAddr).Port
	var b [4]byte
	rand.Read(b[:])
	u.tagBase = hex.EncodeToString(b[:])
	return nil
}

// restore picks up the session of an earlier run when it is still usable.
func (u *UDP) restore() {
	if u.session != "" {
		return
	}
	s, ok, _ := u.State.Get(kvSession)
	if !ok || s == "" {
		return
	}
	used := time.Unix(u.kvInt(kvSessionUsed), 0)
	auth, _, _ := u.State.Get(kvSessionAuth)
	if u.clock().Sub(used) > sessionIdle || int(u.kvInt(kvSessionPort)) != u.port || auth != u.fingerprint() {
		u.forget()
		return
	}
	u.session = s
	if u.Cfg.APIKey != "" {
		salt, _, _ := u.State.Get(kvSalt)
		u.key = encKey(u.Cfg.APIKey, salt)
	}
}

func (u *UDP) forget() {
	u.session, u.key = "", nil
	u.State.Delete(kvSession)
	u.State.Delete(kvSalt)
}

// Call sends an authenticated command and returns its reply. It logs in
// when needed and once more if the server lost the session.
func (u *UDP) Call(ctx context.Context, cmd string, params ...string) (Reply, error) {
	u.callMu.Lock() // the tracker's flush and my list refreshes share one session
	defer u.callMu.Unlock()
	if until := u.kvInt(kvBannedUntil); until > u.clock().Unix() {
		return Reply{}, fmt.Errorf("%w until %s", ErrBanned, time.Unix(until, 0).Format("15:04"))
	}
	if v, ok, _ := u.State.Get(kvLoginBlocked); ok {
		fp, reason, _ := strings.Cut(v, "|")
		if fp == u.fingerprint() {
			return Reply{}, fmt.Errorf("%w (%s)", ErrLoginBlocked, reason)
		}
		u.State.Delete(kvLoginBlocked) // settings changed: try again
	}
	if err := u.dial(); err != nil {
		return Reply{}, err
	}
	u.restore()
	for attempt := 0; ; attempt++ {
		if u.session == "" {
			if err := u.login(ctx); err != nil {
				return Reply{}, err
			}
		}
		r, err := u.send(ctx, cmd, append(params, "s", u.session)...)
		if err != nil {
			return r, err
		}
		if (r.Code == codeLoginFirst || r.Code == codeInvalidSession) && attempt == 0 {
			u.forget()
			continue
		}
		u.State.Set(kvSessionUsed, strconv.FormatInt(u.clock().Unix(), 10))
		return r, nil
	}
}

func (u *UDP) login(ctx context.Context) error {
	u.key = nil
	var salt string
	if u.Cfg.APIKey != "" {
		r, err := u.send(ctx, "ENCRYPT", "user", u.Cfg.Username, "type", "1")
		if err != nil {
			return err
		}
		if r.Code != codeEncryptionEnabled {
			return u.refuse(r)
		}
		salt, _, _ = strings.Cut(r.Text, " ")
		u.key = encKey(u.Cfg.APIKey, salt)
	}
	r, err := u.send(ctx, "AUTH",
		"user", u.Cfg.Username,
		"pass", u.Cfg.Password,
		"protover", "3",
		"client", u.Cfg.Client,
		"clientver", strconv.Itoa(u.Cfg.ClientVersion),
		"nat", "1",
		"enc", "UTF8",
	)
	if err != nil {
		u.key = nil
		return err
	}
	if r.Code != codeLoginAccepted && r.Code != codeLoginAcceptedNew {
		u.key = nil
		return u.refuse(r)
	}
	session, _, _ := strings.Cut(r.Text, " ")
	if session == "" {
		return errors.New("AniDB: login reply without session")
	}
	u.session = session
	u.State.Set(kvSession, session)
	u.State.Set(kvSalt, salt)
	u.State.Set(kvSessionPort, strconv.Itoa(u.port))
	u.State.Set(kvSessionAuth, u.fingerprint())
	u.State.Set(kvSessionUsed, strconv.FormatInt(u.clock().Unix(), 10))
	return nil
}

// refuse turns a failed ENCRYPT/AUTH into an error, and blocks further
// logins for answers that retrying cannot fix: repeated failed logins are
// a quick way to get banned.
func (u *UDP) refuse(r Reply) error {
	err := &PermanentError{Code: r.Code, Text: r.Text}
	switch r.Code {
	case codeLoginFailed, codeClientOutdated, codeClientBanned, codeIllegalInput, 309, 394, 509:
		u.State.Set(kvLoginBlocked, u.fingerprint()+"|"+err.Error())
		return err
	}
	return fmt.Errorf("AniDB login: %d %s", r.Code, r.Text)
}

// send writes one packet, honoring the gap to the previous one (also of
// earlier runs), and waits for the reply with the same tag.
func (u *UDP) send(ctx context.Context, cmd string, params ...string) (Reply, error) {
	u.seq++
	tag := fmt.Sprintf("%s%d", u.tagBase, u.seq)
	var b strings.Builder
	b.WriteString(cmd)
	b.WriteByte(' ')
	for i := 0; i+1 < len(params); i += 2 {
		b.WriteString(params[i])
		b.WriteByte('=')
		b.WriteString(escape(params[i+1]))
		b.WriteByte('&')
	}
	b.WriteString("tag=" + tag)
	pkt := []byte(b.String())
	if u.key != nil {
		pkt = encrypt(u.key, pkt)
	}
	if len(pkt) > 1400 {
		return Reply{}, errors.New("AniDB: packet too large")
	}

	last := time.Unix(0, u.kvInt(kvLastSend))
	if err := u.wait(ctx, last.Add(u.gap()).Sub(u.clock())); err != nil {
		return Reply{}, err
	}
	// record before sending: a crash right after must still count
	u.State.Set(kvLastSend, strconv.FormatInt(u.clock().UnixNano(), 10))
	if _, err := u.conn.Write(pkt); err != nil {
		return Reply{}, fmt.Errorf("AniDB: %w", err)
	}

	deadline := time.Now().Add(u.timeout())
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	buf := make([]byte, 64<<10)
	for {
		u.conn.SetReadDeadline(deadline)
		n, err := u.conn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if ctx.Err() != nil {
					return Reply{}, ctx.Err()
				}
				return Reply{}, ErrNoReply
			}
			return Reply{}, fmt.Errorf("AniDB: %w", err)
		}
		data := buf[:n]
		if u.key != nil {
			// errors such as 506 may come back unencrypted
			if plain, err := decrypt(u.key, data); err == nil {
				data = plain
			}
		}
		r, rtag, ok := parseReply(data)
		if !ok || (rtag != tag && rtag != "") {
			continue // garbage or a late reply to an earlier packet
		}
		if r.Code == codeBanned {
			until := u.clock().Add(banPause).Unix()
			u.State.Set(kvBannedUntil, strconv.FormatInt(until, 10))
			return r, fmt.Errorf("%w: %s", ErrBanned, strings.Join(r.Data, " "))
		}
		return r, nil
	}
}

// parseReply splits "tag code text\ndata...". Some replies (e.g. 598)
// come without our tag.
func parseReply(b []byte) (Reply, string, bool) {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	fields := strings.SplitN(lines[0], " ", 3)
	var r Reply
	var tag string
	switch {
	case len(fields) >= 2 && isCode(fields[1]):
		tag = fields[0]
		r.Code, _ = strconv.Atoi(fields[1])
		if len(fields) == 3 {
			r.Text = fields[2]
		}
	case isCode(fields[0]):
		r.Code, _ = strconv.Atoi(fields[0])
		r.Text = strings.TrimSpace(strings.TrimPrefix(lines[0], fields[0]))
	default:
		return r, "", false
	}
	for _, l := range lines[1:] {
		r.Data = append(r.Data, unescape(l))
	}
	return r, tag, true
}

func isCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	_, err := strconv.Atoi(s)
	return err == nil
}

// AniDB escapes & and newlines in values.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "\r\n", "<br />", "\n", "<br />").Replace(s)
}

func unescape(s string) string {
	return strings.NewReplacer("<br />", "\n", "&amp;", "&").Replace(s)
}

// encKey derives the session key: md5(api key + salt).
func encKey(apiKey, salt string) []byte {
	k := md5.Sum([]byte(apiKey + salt))
	return k[:]
}

// AES-128 in ECB mode with PKCS#5 padding, as the API specifies. ECB is
// weak; it is what the protocol offers and still beats a plaintext
// password.
func encrypt(key, plain []byte) []byte {
	block, _ := aes.NewCipher(key)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	for i := 0; i < len(buf); i += aes.BlockSize {
		block.Encrypt(buf[i:i+aes.BlockSize], buf[i:i+aes.BlockSize])
	}
	return buf
}

func decrypt(key, data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("not encrypted")
	}
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
	}
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || !bytes.Equal(out[len(out)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("bad padding")
	}
	return out[:len(out)-pad], nil
}

// stateRemote is the MyList storage state for new entries: the episode
// was streamed, so it lives on "remote storage".
const stateRemote = "4"

// MarkWatched adds an episode to MyList as watched (a generic entry, no
// file needed, storage state remote), or marks the existing entry watched
// without touching its state.
func (u *UDP) MarkWatched(ctx context.Context, aid, epno int, viewed time.Time) error {
	ep := []string{"aid", strconv.Itoa(aid), "generic", "1", "epno", strconv.Itoa(epno)}
	seen := []string{"viewed", "1", "viewdate", strconv.FormatInt(viewed.Unix(), 10)}
	r, err := u.Call(ctx, "MYLISTADD", slices.Concat(ep, []string{"state", stateRemote}, seen)...)
	if err != nil {
		return err
	}
	switch r.Code {
	case codeMylistAdded:
		// generic adds answer with the number of entries added; 0 means
		// the episode is listed already, so the viewed flag was not set
		if len(r.Data) == 0 || strings.TrimSpace(r.Data[0]) != "0" {
			return nil
		}
		r, err = u.Call(ctx, "MYLISTADD", slices.Concat(ep, []string{"edit", "1"}, seen)...)
		if err != nil {
			return err
		}
		if r.Code == codeMylistEdited {
			return nil
		}
	case codeMylistEdited:
		return nil
	case codeMylistExists:
		// already listed: edit that entry by its lid, the first data field
		lid := ""
		if len(r.Data) > 0 {
			lid, _, _ = strings.Cut(r.Data[0], "|")
		}
		if n, err := strconv.Atoi(lid); err != nil || n <= 0 {
			return &PermanentError{Code: r.Code, Text: "already in MyList, no entry id in reply"}
		}
		r, err = u.Call(ctx, "MYLISTADD", append([]string{"lid", lid, "edit", "1"}, seen...)...)
		if err != nil {
			return err
		}
		if r.Code == codeMylistEdited {
			return nil
		}
	}
	switch r.Code {
	case codeNoSuchFile, codeNoSuchAnime, codeNoSuchGroup, codeNoSuchEntry, codeIllegalInput, codeUnknownCommand:
		return &PermanentError{Code: r.Code, Text: r.Text}
	}
	return fmt.Errorf("AniDB: %d %s", r.Code, r.Text)
}

// MyListProgress returns how many episodes of aid AniDB marks watched, and
// how many episodes the anime has when the reply says. An anime not in
// MyList is 0, 0.
//
// MYLIST aid=N answers 221 with one entry (lid|fid|eid|aid|gid|date|state|
// viewdate|...) or 312 with a summary of many (title|episodes|unknown|hdd|
// cd|deleted|watched episodes|...), where the watched episodes are a set
// like "1-3,5". A reply in neither shape is an error, not a guess: the
// caller keeps showing local progress.
func (u *UDP) MyListProgress(ctx context.Context, aid int) (watched, total int, err error) {
	r, err := u.Call(ctx, "MYLIST", "aid", strconv.Itoa(aid))
	if err != nil {
		return 0, 0, err
	}
	switch r.Code {
	case codeNoSuchEntry, codeNoSuchMylistEntry:
		return 0, 0, nil
	case codeMylist:
		if len(r.Data) == 0 {
			break
		}
		f := strings.Split(r.Data[0], "|")
		if len(f) < 8 {
			break
		}
		if n, err := strconv.Atoi(strings.TrimSpace(f[7])); err == nil && n > 0 {
			return 1, 0, nil
		}
		return 0, 0, nil
	case codeMylistMultiple:
		if len(r.Data) == 0 {
			break
		}
		f := strings.Split(r.Data[0], "|")
		if len(f) < 7 {
			break
		}
		n, ok := countEpisodes(f[6])
		if !ok {
			break
		}
		total, _ = strconv.Atoi(strings.TrimSpace(f[1]))
		return n, total, nil
	case codeNoSuchAnime, codeIllegalInput, codeUnknownCommand:
		return 0, 0, &PermanentError{Code: r.Code, Text: r.Text}
	}
	return 0, 0, fmt.Errorf("AniDB: unrecognised MYLIST reply %d %s", r.Code, r.Text)
}

// countEpisodes counts the episodes of a set such as "1-3,5". Specials
// (S1, C2, ...) are not counted. ok is false when s is not such a set.
func countEpisodes(s string) (n int, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, false
		}
		if part[0] < '0' || part[0] > '9' {
			if strings.ContainsAny(part[:1], "SCTPO") && len(part) > 1 {
				continue // special, credit, trailer, parody, other
			}
			return 0, false
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return 0, false
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return 0, false
			}
		}
		n += b - a + 1
	}
	return n, true
}
