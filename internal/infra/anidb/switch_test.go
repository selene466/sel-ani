package anidb

import (
	"context"
	"errors"
	"testing"
	"time"

	"sel-ani/internal/domain"
)

func TestSwitchOffDoesNothing(t *testing.T) {
	var s Switch
	if err := s.Resolve(context.Background(), domain.Anime{ID: "x"}); err != nil {
		t.Errorf("resolve while off: %v", err)
	}
	if err := s.Watched(context.Background(), domain.Anime{ID: "x"}, "1"); err != nil {
		t.Errorf("watched while off: %v", err)
	}
	if _, _, err := s.MyListProgress(context.Background(), 1); !errors.Is(err, domain.ErrDisabled) {
		t.Errorf("progress while off: %v", err)
	}
	if got := s.Close(time.Second); got != "" {
		t.Errorf("close while off: %q", got)
	}
}
