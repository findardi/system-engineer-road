package main

import (
	"testing"
	"time"
)

func TestSessionExpired(t *testing.T) {
	start := time.Now()
	never := time.Time{}
	for _, c := range []struct {
		name      string
		lastHeard time.Time
		now       time.Time
		want      bool
	}{
		{"waiting for first client", never, start.Add(59 * time.Second), false},
		{"no client came", never, start.Add(61 * time.Second), true},
		{"client idle but connected", start, start.Add(29 * time.Minute), false},
		{"client gone 30 min", start, start.Add(31 * time.Minute), true},
	} {
		if got := sessionExpired(start, c.lastHeard, c.now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseSize(t *testing.T) {
	for _, c := range []struct {
		args []string
		w, h int
	}{
		{nil, 80, 24},
		{[]string{"120x40"}, 120, 40},
		{[]string{"0x40"}, 80, 24},
		{[]string{"5000x40"}, 80, 24},
		{[]string{"junk"}, 80, 24},
	} {
		if w, h := parseSize(c.args); w != c.w || h != c.h {
			t.Errorf("parseSize(%v) = %dx%d, want %dx%d", c.args, w, h, c.w, c.h)
		}
	}
}
