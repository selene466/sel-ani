// Package store keeps sel-ani's state in a SQLite database: watch history,
// the AniDB link of each anime, the AniDB title index, the response cache
// and the queue of pending AniDB updates.
//
// It uses modernc.org/sqlite, a pure Go SQLite, so builds stay static and
// cross-compile without cgo.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"sel-ani/internal/domain"
)

// DB is the opened database. It is safe for concurrent use.
type DB struct {
	db  *sql.DB
	now func() time.Time
}

// appID marks a database as sel-ani's current schema (PRAGMA
// application_id, "SANI"). Databases from before miruro.cx became the only
// source carry other ids and are refused rather than mixed in.
const appID = 0x53414e49

// migrations[i] upgrades schema version i to i+1. Never edit an entry
// once released: append a new one.
var migrations = []string{
	`
CREATE TABLE anime (
	anime_id       TEXT PRIMARY KEY, -- miruro.cx anime id
	title          TEXT NOT NULL,    -- English
	alt_title      TEXT NOT NULL DEFAULT '', -- romaji
	total_episodes INTEGER,          -- NULL: unknown (airing)
	aid            INTEGER,          -- NULL: not decided, 0: no AniDB entry, >0: AniDB aid
	aid_source     TEXT NOT NULL DEFAULT '',
	aid_at         INTEGER
);
CREATE TABLE watch (
	anime_id   TEXT PRIMARY KEY REFERENCES anime(anime_id),
	episode    TEXT NOT NULL,
	watched_at INTEGER NOT NULL
);
CREATE INDEX watch_recent ON watch(watched_at DESC);
CREATE TABLE watch_log (
	anime_id   TEXT NOT NULL REFERENCES anime(anime_id),
	episode    TEXT NOT NULL,
	watched_at INTEGER NOT NULL,
	PRIMARY KEY (anime_id, episode)
);
CREATE TABLE cache (
	key        TEXT PRIMARY KEY,
	value      BLOB NOT NULL,
	fetched_at INTEGER NOT NULL
);
CREATE TABLE kv (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE anidb_title (
	aid   INTEGER NOT NULL,
	type  INTEGER NOT NULL,      -- 1 main, 2 synonym, 3 short, 4 official
	lang  TEXT NOT NULL,
	title TEXT NOT NULL,
	norm  TEXT NOT NULL          -- normalized for matching
);
CREATE INDEX anidb_title_norm ON anidb_title(norm);
CREATE INDEX anidb_title_aid ON anidb_title(aid);
CREATE TABLE anidb_outbox (
	id         INTEGER PRIMARY KEY,
	aid        INTEGER NOT NULL,
	epno       INTEGER NOT NULL,
	viewed_at  INTEGER NOT NULL,
	attempts   INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	next_try   INTEGER NOT NULL DEFAULT 0,
	state      TEXT NOT NULL DEFAULT 'pending', -- pending | done | failed
	UNIQUE (aid, epno)
);
`,
	`
ALTER TABLE anime ADD COLUMN native      TEXT NOT NULL DEFAULT '';
ALTER TABLE anime ADD COLUMN cover_url   TEXT NOT NULL DEFAULT '';
ALTER TABLE anime ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE anime ADD COLUMN format      TEXT NOT NULL DEFAULT '';
ALTER TABLE anime ADD COLUMN status      TEXT NOT NULL DEFAULT '';
DELETE FROM cache WHERE key LIKE 'img:%';
CREATE TABLE image (
	url       TEXT PRIMARY KEY,
	png       BLOB NOT NULL,
	size      INTEGER NOT NULL,
	last_used INTEGER NOT NULL
);
CREATE INDEX image_lru ON image(last_used);
CREATE TABLE mylist (
	aid        INTEGER PRIMARY KEY,
	watched    INTEGER NOT NULL, -- episodes AniDB marks watched
	total      INTEGER NOT NULL, -- episodes AniDB knows, 0 = unknown
	fetched_at INTEGER NOT NULL
);
`,
}

// Open opens or creates the database at path. The file is private (0600):
// it holds the AniDB session.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// create it ourselves so the mode is right from the first byte
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	// journal_mode is set by migrate, after the file is known to be ours:
	// switching to WAL rewrites the header of a file we may refuse
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// one connection: no SQLITE_BUSY between our own goroutines
	sdb.SetMaxOpenConns(1)
	d := &DB{db: sdb, now: time.Now}
	if err := d.migrate(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("database %s: %w", path, err)
	}
	return d, nil
}

func (d *DB) Close() error { return d.db.Close() }

// one scans a single row; ok is false when there is none.
func one[T any](row *sql.Row, dst func(*T) []any) (v T, ok bool, err error) {
	err = row.Scan(dst(&v)...)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

func (d *DB) scalar(q string, args ...any) (string, bool, error) {
	return one(d.db.QueryRow(q, args...), func(s *string) []any { return []any{s} })
}

func (d *DB) migrate() error {
	var v, id, tables int
	if err := d.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if err := d.db.QueryRow(`PRAGMA application_id`).Scan(&id); err != nil {
		return err
	}
	if err := d.db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&tables); err != nil {
		return err
	}
	if id != appID && tables > 0 {
		return errors.New("made by an older sel-ani (before miruro.cx was the only source); delete it to start fresh")
	}
	if v > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this sel-ani (%d)", v, len(migrations))
	}
	if id != appID {
		if _, err := d.db.Exec(fmt.Sprintf(`PRAGMA application_id = %d`, appID)); err != nil {
			return err
		}
	}
	if _, err := d.db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		tx, err := d.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) unix() int64 { return d.now().Unix() }

func (d *DB) Load() ([]domain.HistoryEntry, error) {
	rows, err := d.db.Query(`
		SELECT w.episode, w.anime_id, a.title, a.alt_title, COALESCE(a.aid, 0), COALESCE(a.total_episodes, 0), w.watched_at
		FROM watch w JOIN anime a USING (anime_id)
		ORDER BY w.watched_at DESC, w.rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.HistoryEntry
	for rows.Next() {
		var e domain.HistoryEntry
		var at int64
		if err := rows.Scan(&e.Episode, &e.AnimeID, &e.Title, &e.AltTitle, &e.AID, &e.Total, &at); err != nil {
			return nil, err
		}
		e.WatchedAt = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Save records e as the latest watched episode of its anime.
func (d *DB) Save(e domain.HistoryEntry) error {
	if e.AnimeID == "" || e.Episode == "" {
		return errors.New("history: empty anime id or episode")
	}
	at := e.WatchedAt.Unix()
	if e.WatchedAt.IsZero() {
		at = d.unix()
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertAnime(tx, domain.Anime{ID: e.AnimeID, Title: e.Title, AltTitle: e.AltTitle, Episodes: e.Total}); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO watch (anime_id, episode, watched_at) VALUES (?, ?, ?)
		ON CONFLICT (anime_id) DO UPDATE SET episode = excluded.episode, watched_at = excluded.watched_at`,
		e.AnimeID, e.Episode, at); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO watch_log (anime_id, episode, watched_at) VALUES (?, ?, ?)
		ON CONFLICT (anime_id, episode) DO UPDATE SET watched_at = excluded.watched_at`,
		e.AnimeID, e.Episode, at); err != nil {
		return err
	}
	return tx.Commit()
}

// Clear deletes the watch history. AniDB links, titles, cache and pending
// AniDB updates are kept: they are not history, and rebuilding them costs
// API requests or questions.
func (d *DB) Clear() error {
	_, err := d.db.Exec(`DELETE FROM watch; DELETE FROM watch_log;`)
	return err
}

// WatchedEpisodes lists every episode ever watched of an anime, in
// episode order.
func (d *DB) WatchedEpisodes(animeID string) ([]string, error) {
	rows, err := d.db.Query(`SELECT episode FROM watch_log WHERE anime_id = ? ORDER BY CAST(episode AS REAL), episode`, animeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// WatchTimes maps every episode ever watched of an anime to when it was
// last watched.
func (d *DB) WatchTimes(animeID string) (map[string]time.Time, error) {
	rows, err := d.db.Query(`SELECT episode, watched_at FROM watch_log WHERE anime_id = ?`, animeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var ep string
		var at int64
		if err := rows.Scan(&ep, &at); err != nil {
			return nil, err
		}
		out[ep] = time.Unix(at, 0)
	}
	return out, rows.Err()
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

func upsertAnime(x execer, a domain.Anime) error {
	var total any // NULL: unknown, keeps a known total
	if a.Episodes > 0 {
		total = a.Episodes
	}
	keep := func(col string) string {
		return col + " = CASE WHEN excluded." + col + " <> '' THEN excluded." + col + " ELSE anime." + col + " END"
	}
	_, err := x.Exec(`
		INSERT INTO anime (anime_id, title, alt_title, total_episodes, native, cover_url, description, format, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (anime_id) DO UPDATE SET
			title = excluded.title,
			`+keep("alt_title")+`,
			`+keep("native")+`,
			`+keep("cover_url")+`,
			`+keep("description")+`,
			`+keep("format")+`,
			`+keep("status")+`,
			total_episodes = COALESCE(excluded.total_episodes, anime.total_episodes)`,
		a.ID, a.Title, a.AltTitle, total, a.Native, a.CoverURL, a.Description, a.Format, a.Status)
	return err
}

func (d *DB) RememberAnime(a domain.Anime) error { return upsertAnime(d.db, a) }

func (d *DB) Anime(animeID string) (domain.Anime, bool, error) {
	a, ok, err := one(d.db.QueryRow(`SELECT title, alt_title, COALESCE(total_episodes, 0), native, cover_url, description, format, status
		FROM anime WHERE anime_id = ?`, animeID), func(a *domain.Anime) []any {
		return []any{&a.Title, &a.AltTitle, &a.Episodes, &a.Native, &a.CoverURL, &a.Description, &a.Format, &a.Status}
	})
	a.ID = animeID
	return a, ok, err
}

// AID returns the AniDB id of an anime. decided is false when no match was
// attempted yet; aid 0 with decided means "has no AniDB entry".
func (d *DB) AID(animeID string) (aid int, decided bool, err error) {
	v, ok, err := one(d.db.QueryRow(`SELECT aid FROM anime WHERE anime_id = ?`, animeID), func(v *sql.NullInt64) []any { return []any{v} })
	return int(v.Int64), ok && v.Valid, err
}

func (d *DB) SetAID(animeID string, aid int, source string) error {
	res, err := d.db.Exec(`UPDATE anime SET aid = ?, aid_source = ?, aid_at = ? WHERE anime_id = ?`, aid, source, d.unix(), animeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = d.db.Exec(`INSERT INTO anime (anime_id, title, aid, aid_source, aid_at) VALUES (?, '', ?, ?, ?)`, animeID, aid, source, d.unix())
	}
	return err
}

// CacheGet returns the value of key when it is younger than maxAge.
func (d *DB) CacheGet(key string, maxAge time.Duration) ([]byte, bool, error) {
	type row struct {
		v  []byte
		at int64
	}
	r, ok, err := one(d.db.QueryRow(`SELECT value, fetched_at FROM cache WHERE key = ?`, key), func(r *row) []any { return []any{&r.v, &r.at} })
	if !ok || err != nil || d.now().Sub(time.Unix(r.at, 0)) > maxAge {
		return nil, false, err
	}
	return r.v, true, nil
}

func (d *DB) CachePut(key string, v []byte) error {
	_, err := d.db.Exec(`
		INSERT INTO cache (key, value, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, fetched_at = excluded.fetched_at`,
		key, v, d.unix())
	return err
}

// PruneCache drops entries older than maxAge.
func (d *DB) PruneCache(maxAge time.Duration) error {
	_, err := d.db.Exec(`DELETE FROM cache WHERE fetched_at < ?`, d.now().Add(-maxAge).Unix())
	return err
}

func (d *DB) Get(key string) (string, bool, error) {
	return d.scalar(`SELECT value FROM kv WHERE key = ?`, key)
}

func (d *DB) Set(key, value string) error { return setKV(d.db, key, value) }

func setKV(x execer, key, value string) error {
	_, err := x.Exec(`INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (d *DB) Delete(key string) error {
	_, err := d.db.Exec(`DELETE FROM kv WHERE key = ?`, key)
	return err
}

// Acquire takes or renews the named lease for owner. Two sel-ani processes
// must not talk to AniDB at the same time: together they would break the
// rate limit.
func (d *DB) Acquire(name, owner string, ttl time.Duration) (bool, error) {
	key, now := "lease:"+name, d.unix()
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var v string
	if err := tx.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&v); err == nil {
		holder, expStr, _ := strings.Cut(v, "|")
		if exp, _ := strconv.ParseInt(expStr, 10, 64); holder != owner && exp > now {
			return false, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err := setKV(tx, key, fmt.Sprintf("%s|%d", owner, now+int64(ttl.Seconds()))); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (d *DB) Release(name, owner string) error {
	_, err := d.db.Exec(`DELETE FROM kv WHERE key = ? AND substr(value, 1, ?) = ?`, "lease:"+name, len(owner)+1, owner+"|")
	return err
}

type Title struct {
	AID   int
	Type  int // 1 main, 2 synonym, 3 short, 4 official
	Lang  string
	Title string
	Norm  string
}

// ReplaceTitles swaps the whole title index in one transaction: a failed
// download or parse keeps the previous index.
func (d *DB) ReplaceTitles(each func(yield func(Title) error) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM anidb_title`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO anidb_title (aid, type, lang, title, norm) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	if err := each(func(t Title) error {
		_, err := stmt.Exec(t.AID, t.Type, t.Lang, t.Title, t.Norm)
		return err
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) TitleCount() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT count(*) FROM anidb_title`).Scan(&n)
	return n, err
}

func (d *DB) queryTitles(q string, args ...any) ([]Title, error) {
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Title
	for rows.Next() {
		var t Title
		if err := rows.Scan(&t.AID, &t.Type, &t.Lang, &t.Title, &t.Norm); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) ExactTitles(norm string) ([]Title, error) {
	return d.queryTitles(`SELECT aid, type, lang, title, norm FROM anidb_title WHERE norm = ?`, norm)
}

// TitlesLike returns titles containing the normalized word.
func (d *DB) TitlesLike(word string, limit int) ([]Title, error) {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(word)
	return d.queryTitles(`SELECT aid, type, lang, title, norm FROM anidb_title WHERE norm LIKE ? ESCAPE '\' LIMIT ?`, "%"+esc+"%", limit)
}

// MainTitle returns the title to show for aid: the main romaji title,
// else any romaji title, else Japanese, else English, else anything.
func (d *DB) MainTitle(aid int) (string, error) {
	s, _, err := d.scalar(`SELECT title FROM anidb_title WHERE aid = ?
		ORDER BY (lang = 'x-jat' AND type = 1) DESC, lang = 'x-jat' DESC, lang = 'ja' DESC, lang = 'en' DESC,
			type = 1 DESC, type = 4 DESC, rowid
		LIMIT 1`, aid)
	return s, err
}

// OutboxItem is a pending "episode watched" update for AniDB.
type OutboxItem struct {
	ID        int64
	AID       int
	EpNo      int
	ViewedAt  time.Time
	Attempts  int
	LastError string
}

// Enqueue queues an episode as watched. An episode already queued, sent or
// rejected is not queued again: watching twice costs no extra API request.
func (d *DB) Enqueue(aid, epno int, viewedAt time.Time) (bool, error) {
	res, err := d.db.Exec(`INSERT OR IGNORE INTO anidb_outbox (aid, epno, viewed_at) VALUES (?, ?, ?)`, aid, epno, viewedAt.Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DueOutbox returns pending items whose retry time has come, oldest first.
func (d *DB) DueOutbox(limit int) ([]OutboxItem, error) {
	rows, err := d.db.Query(`
		SELECT id, aid, epno, viewed_at, attempts, last_error FROM anidb_outbox
		WHERE state = 'pending' AND next_try <= ? ORDER BY id LIMIT ?`, d.unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		var at int64
		if err := rows.Scan(&it.ID, &it.AID, &it.EpNo, &at, &it.Attempts, &it.LastError); err != nil {
			return nil, err
		}
		it.ViewedAt = time.Unix(at, 0)
		out = append(out, it)
	}
	return out, rows.Err()
}

func (d *DB) PendingCount() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT count(*) FROM anidb_outbox WHERE state = 'pending'`).Scan(&n)
	return n, err
}

func (d *DB) OutboxDone(id int64) error {
	_, err := d.db.Exec(`UPDATE anidb_outbox SET state = 'done', last_error = '' WHERE id = ?`, id)
	return err
}

// OutboxRetry records a transient failure and when to try again.
func (d *DB) OutboxRetry(id int64, msg string, next time.Time) error {
	_, err := d.db.Exec(`UPDATE anidb_outbox SET attempts = attempts + 1, last_error = ?, next_try = ? WHERE id = ?`, msg, next.Unix(), id)
	return err
}

// OutboxFailed records a permanent failure: the item is never sent again.
func (d *DB) OutboxFailed(id int64, msg string) error {
	_, err := d.db.Exec(`UPDATE anidb_outbox SET state = 'failed', attempts = attempts + 1, last_error = ? WHERE id = ?`, msg, id)
	return err
}

// Progress maps each anime id to its last watched episode: from the watch
// history, else "" for anime linked to AniDB.
func (d *DB) Progress() (map[string]string, error) {
	rows, err := d.db.Query(`
		SELECT anime_id, episode FROM watch
		UNION ALL
		SELECT anime_id, '' FROM anime a WHERE aid > 0 AND NOT EXISTS (SELECT 1 FROM watch w WHERE w.anime_id = a.anime_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, ep string
		if err := rows.Scan(&id, &ep); err != nil {
			return nil, err
		}
		out[id] = ep
	}
	return out, rows.Err()
}

// MyList returns every anime with an AniDB link (aid > 0).
func (d *DB) MyList() ([]domain.ListEntry, error) {
	rows, err := d.db.Query(`
		SELECT a.anime_id, a.title, a.alt_title, COALESCE(a.total_episodes, 0), a.native, a.cover_url,
		       a.description, a.format, a.status, a.aid,
		       COALESCE(w.episode, ''), COALESCE(w.watched_at, 0),
		       m.aid IS NOT NULL, COALESCE(m.watched, 0), COALESCE(m.total, 0), COALESCE(m.fetched_at, 0)
		FROM anime a
		LEFT JOIN watch w USING (anime_id)
		LEFT JOIN mylist m ON m.aid = a.aid
		WHERE a.aid > 0
		ORDER BY COALESCE(w.watched_at, 0) DESC, a.rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ListEntry
	for rows.Next() {
		var e domain.ListEntry
		var at, synced int64
		a := &e.Anime
		if err := rows.Scan(&a.ID, &a.Title, &a.AltTitle, &a.Episodes, &a.Native, &a.CoverURL, &a.Description,
			&a.Format, &a.Status, &e.AID, &e.Watched, &at, &e.Synced, &e.AniDBWatched, &e.AniDBTotal, &synced); err != nil {
			return nil, err
		}
		if at > 0 {
			e.WatchedAt = time.Unix(at, 0)
		}
		if synced > 0 {
			e.SyncedAt = time.Unix(synced, 0)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) PutMyList(aid, watched, total int) error {
	_, err := d.db.Exec(`
		INSERT INTO mylist (aid, watched, total, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (aid) DO UPDATE SET watched = excluded.watched, total = excluded.total, fetched_at = excluded.fetched_at`,
		aid, watched, total, d.unix())
	return err
}

// ImageGet returns a stored cover and marks it as used now.
func (d *DB) ImageGet(url string) ([]byte, bool, error) {
	v, ok, err := one(d.db.QueryRow(`SELECT png FROM image WHERE url = ?`, url), func(v *[]byte) []any { return []any{v} })
	if ok {
		_, _ = d.db.Exec(`UPDATE image SET last_used = ? WHERE url = ?`, d.unix(), url)
	}
	return v, ok, err
}

// ImagePut stores a cover, then evicts the least recently used ones until
// the images take at most maxBytes.
func (d *DB) ImagePut(url string, png []byte, maxBytes int64) error {
	_, err := d.db.Exec(`
		INSERT INTO image (url, png, size, last_used) VALUES (?, ?, ?, ?)
		ON CONFLICT (url) DO UPDATE SET png = excluded.png, size = excluded.size, last_used = excluded.last_used`,
		url, png, len(png), d.unix())
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`
		DELETE FROM image WHERE url IN (
			SELECT url FROM (
				SELECT url, SUM(size) OVER (ORDER BY last_used DESC, rowid DESC) AS running FROM image
			) WHERE running > ? AND url <> ?
		)`, maxBytes, url)
	return err
}

// Compact gives space freed by deletes back to the file system. It rewrites
// the file only when over a quarter of it is free pages.
func (d *DB) Compact() error {
	var free, pages int
	if err := d.db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		return err
	}
	if err := d.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		return err
	}
	if pages == 0 || free*4 < pages {
		return nil
	}
	if _, err := d.db.Exec(`VACUUM`); err != nil {
		return err
	}
	_, err := d.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}
