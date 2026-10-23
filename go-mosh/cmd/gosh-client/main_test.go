package main

import (
	"testing"
	"time"
)

func TestNotice(t *testing.T) {
	now := time.Now()
	help := message{`Commands: "." quits, "^" gives literal Ctrl-^`, now.Add(time.Hour), false}
	relay := message{"No UDP reply, using SSH relay.", now.Add(time.Second), true}
	for _, c := range []struct {
		m         message
		lastHeard time.Time
		want      string
	}{
		{message{}, now.Add(-2 * time.Second), ""},
		{message{}, time.Time{}, ""}, // never heard yet: no countdown
		{message{}, now.Add(-12 * time.Second), "gosh: Last contact 12 seconds ago. [To quit: Ctrl-^ .]"},
		{message{}, now.Add(-125 * time.Second), "gosh: Last contact 2:05 ago. [To quit: Ctrl-^ .]"},
		{help, now, `gosh: Commands: "." quits, "^" gives literal Ctrl-^`},
		{relay, now, "gosh: No UDP reply, using SSH relay. [To quit: Ctrl-^ .]"},
		{relay, now.Add(-8 * time.Second), "gosh: No UDP reply, using SSH relay. (8 s without contact.) [To quit: Ctrl-^ .]"},
		{message{"old", now.Add(-time.Second), true}, now, ""}, // expired
	} {
		if got := notice(c.m, c.lastHeard, now); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{5 * time.Second: "5 seconds", 61 * time.Second: "1:01", 3723 * time.Second: "1:02:03"} {
		if got := humanDuration(d, "seconds"); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
