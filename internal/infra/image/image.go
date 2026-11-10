// Package image fetches cover images and converts them to PNG, the only
// format the kitty graphics protocol takes directly. Results are cached
// forever: a cover url's content does not change.
package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"

	"sel-ani/internal/infra/fetch"
)

// ErrNoImage is returned for an empty url.
var ErrNoImage = errors.New("no image url")

// Cache is the persistence the source needs (implemented by store.DB).
type Cache interface {
	ImageGet(url string) ([]byte, bool, error)
	ImagePut(url string, png []byte, maxBytes int64) error
}

const (
	// MaxWidth is the widest stored cover. The preview pane shows a cover
	// at most a few hundred pixels wide, so more only costs disk.
	MaxWidth = 300
	// MaxCacheBytes caps the stored covers.
	MaxCacheBytes = 50 << 20
)

// Source loads images through a fetcher and a cache.
type Source struct {
	Fetcher fetch.Fetcher
	Cache   Cache
}

// PNG returns the image at url as PNG bytes.
func (s *Source) PNG(ctx context.Context, url string) ([]byte, error) {
	if url == "" {
		return nil, ErrNoImage
	}
	if b, ok, err := s.Cache.ImageGet(url); err == nil && ok {
		return b, nil
	}
	raw, err := s.Fetcher.Fetch(ctx, url, "")
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", url, err)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, shrink(img, MaxWidth)); err != nil {
		return nil, err
	}
	_ = s.Cache.ImagePut(url, out.Bytes(), MaxCacheBytes)
	return out.Bytes(), nil
}

// Size returns the pixel size of a PNG, 0, 0 when it is not one.
func Size(pngBytes []byte) (w, h int) {
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// shrink scales img down to at most maxW pixels wide by averaging boxes of
// source pixels. Smaller images are returned as they are.
func shrink(img image.Image, maxW int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW || w == 0 || h == 0 {
		return img
	}
	nw, nh := maxW, max(h*maxW/w, 1)
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := range nh {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+max((y+1)*h/nh, y*h/nh+1)
		for x := range nw {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+max((x+1)*w/nw, x*w/nw+1)
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := img.At(xx, yy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			out.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), uint8(a / n >> 8)})
		}
	}
	return out
}
