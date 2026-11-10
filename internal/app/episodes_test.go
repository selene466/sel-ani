package app

import (
	"errors"
	"reflect"
	"testing"

	"sel-ani/internal/domain"
)

var eps = []string{"1", "2", "3", "4", "5", "5.5", "6"}

func TestResolveEpisodes(t *testing.T) {
	tests := []struct {
		spec string
		want []string
		err  error
	}{
		{"3", []string{"3"}, nil},
		{"5.5", []string{"5.5"}, nil},
		{"-1", []string{"6"}, nil},
		{"2-4", []string{"2", "3", "4"}, nil},
		{"2 4", []string{"2", "3", "4"}, nil},
		{"5-6", []string{"5", "5.5", "6"}, nil},
		{"0-2", []string{"1", "2"}, nil},
		{"4--1", []string{"4", "5", "5.5", "6"}, nil},
		{"4 -1", []string{"4", "5", "5.5", "6"}, nil},
		{"3-3", []string{"3"}, nil},
		{" 3 ", []string{"3"}, nil},
		{"9", nil, domain.ErrInvalidEp},
		{"2-9", nil, domain.ErrInvalidRng},
		{"4-2", nil, domain.ErrInvalidRng},
		{"abc", nil, domain.ErrInvalidEp},
		{"", nil, domain.ErrInvalidEp},
	}
	for _, tt := range tests {
		got, err := ResolveEpisodes(eps, tt.spec)
		if !errors.Is(err, tt.err) {
			t.Errorf("ResolveEpisodes(%q) err = %v, want %v", tt.spec, err, tt.err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ResolveEpisodes(%q) = %v, want %v", tt.spec, got, tt.want)
		}
	}
}

func TestNextPrev(t *testing.T) {
	if got, ok := Next(eps, "5"); !ok || got != "5.5" {
		t.Errorf("Next(5) = %q %v", got, ok)
	}
	if _, ok := Next(eps, "6"); ok {
		t.Error("Next(last) should fail")
	}
	if _, ok := Next(eps, "42"); ok {
		t.Error("Next(unknown) should fail")
	}
	if got, ok := Prev(eps, "2"); !ok || got != "1" {
		t.Errorf("Prev(2) = %q %v", got, ok)
	}
	if _, ok := Prev(eps, "1"); ok {
		t.Error("Prev(first) should fail")
	}
}
