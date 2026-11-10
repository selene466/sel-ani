package miruro

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"sel-ani/internal/domain"
	"sel-ani/internal/infra/fetch"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const (
	base    = "https://miruro.cx"
	animeID = "vtupV7d-fI4muvuKv11-3-_mTsTFvLjA"
	watch   = "/watch/" + animeID + "/black-clover-season-2"
)

// fakeFetcher serves site fixtures by url and the master playlist for
// stream hosts whose name contains one of ok.
type fakeFetcher struct {
	t       *testing.T
	pages   map[string]string
	ok      []string
	calls   []string
	refs    map[string]string
	origins map[string]string
}

func (f *fakeFetcher) Fetch(ctx context.Context, u, ref string) ([]byte, error) {
	f.calls = append(f.calls, u)
	if f.refs == nil {
		f.refs = map[string]string{}
	}
	f.refs[u] = ref
	if name, ok := f.pages[u]; ok {
		return fixture(f.t, name), nil
	}
	if strings.HasPrefix(u, base) {
		return nil, fmt.Errorf("unexpected fetch %s", u)
	}
	if f.origins == nil {
		f.origins = map[string]string{}
	}
	f.origins[u] = fetch.OriginFrom(ctx)
	for _, h := range f.ok {
		if strings.Contains(u, h) {
			return fixture(f.t, "master.m3u8"), nil
		}
	}
	return nil, errors.New("request failed: HTTP 403")
}

func newFake(t *testing.T, ok ...string) *fakeFetcher {
	return &fakeFetcher{t: t, ok: ok, pages: map[string]string{
		base + "/search/__data.json?q=frieren":      "search.json",
		base + "/__data.json":                       "home.json",
		base + "/watch/" + animeID + "/__data.json": "redirect.json",
		base + watch + "/__data.json":               "watch.json",
		base + watch + "/__data.json?ep=1":          "watch.json",
	}}
}

func TestDecodePage(t *testing.T) {
	p, err := decodePage(fixture(t, "watch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Nodes) != 2 || p.Redirect != "" {
		t.Fatalf("page = %+v", p)
	}
	n := p.last().(map[string]any)
	eps := n["episodesData"].(map[string]any)["episodes"].([]any)
	if len(eps) != 4 {
		t.Errorf("promise not resolved: %v", n["episodesData"])
	}

	p, err = decodePage(fixture(t, "redirect.json"))
	if err != nil || p.Redirect != watch+"?ep=1" {
		t.Errorf("redirect = %+v %v", p, err)
	}

	for name, in := range map[string]string{
		"server error":   `{"type":"error","error":{"message":"boom"}}`,
		"missing chunk":  `{"type":"data","nodes":[{"type":"data","data":[{"a":1},["Promise",2],7]}]}`,
		"bad reference":  `{"type":"data","nodes":[{"type":"data","data":[{"a":9}]}]}`,
		"self reference": `{"type":"data","nodes":[{"type":"data","data":[[0]]}]}`,
		"not json":       `<html>`,
		"no data":        ``,
	} {
		if _, err := decodePage([]byte(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	// undefined fields and skipped nodes
	p, err = decodePage([]byte(`{"type":"data","nodes":[null,{"type":"skip"},{"type":"data","data":[{"a":-1,"b":1},"x"]}]}`))
	if err != nil || !reflect.DeepEqual(p.Nodes, []any{nil, nil, map[string]any{"a": nil, "b": "x"}}) {
		t.Errorf("nodes = %#v %v", p.Nodes, err)
	}
}

func TestSearch(t *testing.T) {
	pg, err := New(newFake(t), base, nil).Search(context.Background(), "frieren", "")
	if err != nil {
		t.Fatal(err)
	}
	got := pg.Items
	if len(got) != 3 {
		t.Fatalf("got %d results", len(got))
	}
	want := domain.Anime{ID: "wHWR9m4VQ8GLW4GP9_ZTblQXn9f0phB8", Title: "Frieren: Beyond Journey’s End", AltTitle: "Sousou no Frieren", Native: "葬送のフリーレン", Format: "TV", Status: "FINISHED", Episodes: 28, Sub: 28, Dub: 28}
	if pg.Next != "" {
		t.Errorf("hasMore with fixture: %q", pg.Next)
	}
	if got[0] != want {
		t.Errorf("got %+v", got[0])
	}
}

// home.json is a live capture: one "latest" rail per audio track.
func TestLatest(t *testing.T) {
	p := New(newFake(t), base, nil)
	sub, err := p.Latest(context.Background(), domain.ModeSub)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) != 54 || sub[0].ID != "sQsUsMbWyZMGoFsqbGsxSpfgGsIKJtiy" || sub[0].AltTitle != "Meitantei Precure!" || sub[0].Sub != 40 {
		t.Errorf("sub: %d items, first %+v", len(sub), sub[0])
	}
	dub, err := p.Latest(context.Background(), domain.ModeDub)
	if err != nil {
		t.Fatal(err)
	}
	if len(dub) != 47 || dub[0].AltTitle != "Kyoufu Collector" || dub[0].Dub != 2 {
		t.Errorf("dub: %d items, first %+v", len(dub), dub[0])
	}
}

func TestEpisodes(t *testing.T) {
	got, err := New(newFake(t), base, nil).Episodes(context.Background(), animeID)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Episode{
		{ID: watch + "?ep=1", Number: "1"},
		{ID: watch + "?ep=2", Number: "2"},
		{ID: watch + "?ep=3", Number: "3"},
		{ID: watch + "?ep=3.5", Number: "3.5"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	// a redirect loop
	f := newFake(t)
	f.pages[base+watch+"/__data.json?ep=1"] = "redirect.json"
	if _, err := New(f, base, nil).Episodes(context.Background(), animeID); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("redirect loop: %v", err)
	}
	for _, id := range []string{"", "a/b", "x?y"} {
		if _, err := New(newFake(t), base, nil).Episodes(context.Background(), id); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}

// sunFirst is the order the fixture cases below were written for.
var sunFirst = []string{"sun", "bee", "bun"}

func TestStreamPreference(t *testing.T) {
	ep := domain.Episode{ID: watch + "?ep=1", Number: "1"}
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		servers []string
		mode    domain.Mode
		ok      []string // reachable stream hosts
		host    string   // host of the chosen link
		referer string
		origin  string
		sub     bool
	}{
		// sun's hard-sub track first
		{"sub on sun", sunFirst, domain.ModeSub, []string{"pearlwinter", "nexabloom"}, "pearlwinter", "https://ultracloud.cc/", "https://ultracloud.cc", false},
		// sun's hard-sub hosts down: its second server, then soft sub
		{"sun second server", sunFirst, domain.ModeSub, []string{"sunwaveclear"}, "sunwaveclear", "https://ultracloud.cc/", "https://ultracloud.cc", false},
		// sun unreachable: bee soft sub, with the subtitle file
		{"fallback to bee", sunFirst, domain.ModeSub, []string{"nexabloom"}, "nexabloom", "https://megaplay.buzz/", "", true},
		// hard sub wins over server order: bee has soft sub only, sun hard sub
		{"hard sub first", []string{"bee", "sun"}, domain.ModeSub, []string{"pearlwinter", "nexabloom"}, "pearlwinter", "https://ultracloud.cc/", "https://ultracloud.cc", false},
		{"dub on bee", []string{"bee", "sun"}, domain.ModeDub, []string{"pearlwinter", "nexabloom"}, "nexabloom", "https://megaplay.buzz/", "", false},
		// defaults: bun is embed only, so sun
		{"defaults skip bun", DefaultServers, domain.ModeSub, []string{"pearlwinter", "nexabloom"}, "pearlwinter", "https://ultracloud.cc/", "https://ultracloud.cc", false},
		// no servers configured: the site's own order (sun first)
		{"site order", nil, domain.ModeDub, []string{"pearlwinter"}, "pearlwinter", "https://ultracloud.cc/", "https://ultracloud.cc", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, c.ok...)
			st, err := New(f, base, c.servers).Stream(ctx, ep, c.mode)
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Links) != 3 || !strings.Contains(st.Links[0].URL, c.host) || st.Links[0].Quality != "1080p" {
				t.Errorf("links = %+v", st.Links)
			}
			if st.Referer != c.referer || st.MalID != "61967" || (st.Subtitle != "") != c.sub {
				t.Errorf("stream = %+v", st)
			}
			for u, ref := range f.refs {
				if strings.Contains(u, c.host) && ref != c.referer {
					t.Errorf("%s fetched with referer %q", u, ref)
				}
				if strings.Contains(u, c.host) && f.origins[u] != c.origin {
					t.Errorf("%s fetched with origin %q", u, f.origins[u])
				}
			}
			if st.Origin != c.origin {
				t.Errorf("stream origin %q, want %q", st.Origin, c.origin)
			}
		})
	}
}

func TestDefaultServers(t *testing.T) {
	if want := []string{"bun", "sun", "bee", "hop"}; !reflect.DeepEqual(DefaultServers, want) {
		t.Errorf("DefaultServers = %q, want %q", DefaultServers, want)
	}
}

func TestStreamNoSources(t *testing.T) {
	ep := domain.Episode{ID: watch + "?ep=1", Number: "1"}
	// bun only offers embeds, everything else is down
	_, err := New(newFake(t), base, sunFirst).Stream(context.Background(), ep, domain.ModeSub)
	if !errors.Is(err, domain.ErrNoSources) || !strings.Contains(err.Error(), "bun/ssub: embed only") {
		t.Errorf("err = %v", err)
	}
	_, err = New(newFake(t, "pearlwinter"), base, []string{"nope"}).Stream(context.Background(), ep, domain.ModeSub)
	if !errors.Is(err, domain.ErrNoSources) {
		t.Errorf("unknown server: err = %v", err)
	}
	for _, id := range []string{"1", "/watch/x", "/other/x?ep=1"} {
		if _, err := New(newFake(t), base, nil).Stream(context.Background(), domain.Episode{ID: id}, domain.ModeSub); err == nil {
			t.Errorf("episode id %q accepted", id)
		}
	}
}

func TestSubtitlePrefersEnglish(t *testing.T) {
	var o offered
	o.Subtitles = append(o.Subtitles,
		struct {
			Language string `json:"language"`
			Label    string `json:"label"`
			File     string `json:"file"`
			Default  bool   `json:"default"`
		}{"ara", "Arabic", "ar.vtt", true},
		struct {
			Language string `json:"language"`
			Label    string `json:"label"`
			File     string `json:"file"`
			Default  bool   `json:"default"`
		}{"eng", "English", "en.vtt", false},
	)
	if got := subtitle(o); got != "en.vtt" {
		t.Errorf("got %s", got)
	}
	o.Subtitles = o.Subtitles[:1]
	if got := subtitle(o); got != "ar.vtt" {
		t.Errorf("no english: got %s", got)
	}
}

// search_live.json / search_page2.json are live captures of a paged search.
func TestSearchPaging(t *testing.T) {
	f := newFake(t)
	f.pages[base+"/search/__data.json?q=live"] = "search_live.json"
	p := New(f, base, nil)
	p.SearchGap = 0
	p1, err := p.Search(context.Background(), "live", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != 24 || !strings.HasPrefix(p1.Next, "c1_") {
		t.Fatalf("page 1: %d items, next %q", len(p1.Items), p1.Next)
	}
	if p1.Items[0].CoverURL == "" {
		t.Error("no cover url")
	}
	f.pages[base+"/search/__data.json?cursor="+p1.Next+"&q=live"] = "search_page2.json"
	p2, err := p.Search(context.Background(), "live", p1.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) != 17 || p2.Next != "" {
		t.Errorf("page 2: %d items, next %q", len(p2.Items), p2.Next)
	}
}

func TestInfo(t *testing.T) {
	f := newFake(t)
	f.pages[base+watch+"/__data.json?ep=1"] = "info.json"
	a, err := New(f, base, nil).Info(context.Background(), animeID)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "anime-id" || a.AltTitle != "Sousou no Frieren" || a.Native != "葬送のフリーレン" || a.Format != "TV" ||
		a.Status != "FINISHED" || a.Episodes != 28 || a.CoverURL != "https://img/xl.jpg" || a.Description != "The adventure is over." || a.Sub != 28 {
		t.Errorf("%+v", a)
	}
}

func TestInfoWithoutMedia(t *testing.T) {
	f := newFake(t)
	f.pages[base+watch+"/__data.json?ep=1"] = "search.json" // a page without a media object
	if _, err := New(f, base, nil).Info(context.Background(), animeID); err == nil {
		t.Error("a page without details must be an error")
	}
}

func TestConfigureChangesTheSiteAndServers(t *testing.T) {
	f := newFake(t)
	p := New(f, base, []string{"sun"})
	f.pages["https://mirror.example/__data.json"] = "home.json"
	p.Configure("https://mirror.example", []string{"bee"})
	if _, err := p.Latest(context.Background(), domain.ModeSub); err != nil {
		t.Fatalf("the mirror is not used: %v", err)
	}
	if got := p.serverOrder(); len(got) != 1 || got[0] != "bee" {
		t.Errorf("%v", got)
	}
}

func TestSearchPagesArePaced(t *testing.T) {
	f := newFake(t)
	f.pages[base+"/search/__data.json?q=live"] = "search_live.json"
	p := New(f, base, nil)
	p.SearchGap = 150 * time.Millisecond
	t0 := time.Now()
	p1, err := p.Search(context.Background(), "live", "")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(t0) > 100*time.Millisecond {
		t.Error("the first page must not wait")
	}
	f.pages[base+"/search/__data.json?cursor="+p1.Next+"&q=live"] = "search_page2.json"
	t1 := time.Now()
	if _, err := p.Search(context.Background(), "live", p1.Next); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t1); d < 100*time.Millisecond {
		t.Errorf("the next page came after %v, want the gap", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Search(ctx, "live", p1.Next); err == nil {
		t.Error("a cancelled wait must stop")
	}
}
