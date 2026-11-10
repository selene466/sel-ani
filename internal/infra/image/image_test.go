package image

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

type fakeFetch struct {
	body  []byte
	calls int
}

func (f *fakeFetch) Fetch(context.Context, string, string) ([]byte, error) {
	f.calls++
	return f.body, nil
}

type memCache map[string][]byte

func (m memCache) ImageGet(k string) ([]byte, bool, error) {
	v, ok := m[k]
	return v, ok, nil
}
func (m memCache) ImagePut(k string, v []byte, _ int64) error { m[k] = v; return nil }

func jpg(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 6))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPNGConvertsAndCaches(t *testing.T) {
	f := &fakeFetch{body: jpg(t)}
	s := &Source{Fetcher: f, Cache: memCache{}}
	out, err := s.PNG(context.Background(), "http://x/c.jpg")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != 4 || cfg.Height != 6 {
		t.Fatalf("not a 4x6 png: %+v %v", cfg, err)
	}
	if _, err := s.PNG(context.Background(), "http://x/c.jpg"); err != nil || f.calls != 1 {
		t.Errorf("second call hit the network: %d calls, %v", f.calls, err)
	}
}

func TestPNGRejectsGarbage(t *testing.T) {
	c := memCache{}
	s := &Source{Fetcher: &fakeFetch{body: []byte("<html>")}, Cache: c}
	if _, err := s.PNG(context.Background(), "http://x"); err == nil {
		t.Fatal("garbage accepted")
	}
	if len(c) != 0 {
		t.Error("garbage cached")
	}
	if _, err := s.PNG(context.Background(), ""); !errors.Is(err, ErrNoImage) {
		t.Errorf("empty url: %v", err)
	}
}

func TestSize(t *testing.T) {
	f := &fakeFetch{body: jpg(t)}
	s := &Source{Fetcher: f, Cache: memCache{}}
	out, _ := s.PNG(context.Background(), "u")
	if w, h := Size(out); w != 4 || h != 6 {
		t.Errorf("%dx%d", w, h)
	}
	if w, h := Size([]byte("x")); w != 0 || h != 0 {
		t.Error("garbage size")
	}
}

func TestLargeCoversAreShrunk(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 920, 1300))
	for y := 0; y < 1300; y++ {
		for x := 0; x < 920; x++ {
			big.Set(x, y, color.RGBA{200, 100, 50, 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, big, nil)
	s := &Source{Fetcher: &fakeFetch{body: b.Bytes()}, Cache: memCache{}}
	out, err := s.PNG(context.Background(), "u")
	if err != nil {
		t.Fatal(err)
	}
	if w, h := Size(out); w != MaxWidth || h != 423 {
		t.Errorf("%dx%d", w, h)
	}
	img, _ := png.Decode(bytes.NewReader(out))
	r, g, bl, _ := img.At(10, 10).RGBA()
	if r>>8 < 190 || r>>8 > 210 || g>>8 < 90 || g>>8 > 110 || bl>>8 < 40 || bl>>8 > 60 {
		t.Errorf("colors changed: %d %d %d", r>>8, g>>8, bl>>8)
	}
}
