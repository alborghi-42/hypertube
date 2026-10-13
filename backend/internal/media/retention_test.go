package media

import (
	"testing"
	"time"
)

func TestInfohashDir(t *testing.T) {
	ih := "0123456789abcdef0123456789abcdef01234567"
	cases := map[string]bool{
		ih + "/movie/film.mkv": true,
		ih:                     true,
		"":                     false,
		"../etc/passwd":        false,
		"zz23456789abcdef0123456789abcdef01234567/x": false,
	}
	for in, want := range cases {
		got, ok := infohashDir(in)
		if ok != want || (ok && got != ih) {
			t.Errorf("infohashDir(%q) = %q, %v; want ok=%v", in, got, ok, want)
		}
	}
}

func TestWatchTracker(t *testing.T) {
	w := newWatchTracker()
	done1 := w.begin("a")
	done2 := w.begin("a")
	if !w.busy("a") || w.busy("b") {
		t.Fatal("busy mismatch")
	}
	done1()
	if !w.busy("a") {
		t.Fatal("a released too early")
	}
	done2()
	if w.busy("a") {
		t.Fatal("a still busy")
	}

	now := time.Now()
	if !w.shouldTouch("a", now) {
		t.Fatal("first touch must be due")
	}
	if w.shouldTouch("a", now.Add(30*time.Second)) {
		t.Fatal("touch within throttle window")
	}
	if !w.shouldTouch("a", now.Add(touchThrottle)) {
		t.Fatal("touch after throttle window must be due")
	}
	w.prune(now.Add(3 * touchThrottle))
	if len(w.touched) != 0 {
		t.Fatal("prune left entries")
	}
}
