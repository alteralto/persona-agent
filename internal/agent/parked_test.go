package agent

import (
	"testing"
	"time"
)

func TestParkedInAir(t *testing.T) {
	a := &Agent{}
	t0 := time.Now()
	if !a.parkedInAir("Chochok", "-446.0,81.6,-155.6", t0) {
		t.Fatal("joined at first sight, before knowing whether they fly anywhere")
	}
	if a.parkedInAir("Chochok", "-445.0,81.9,-155.0", t0.Add(5*time.Second)) {
		t.Fatal("parked after 5 s")
	}
	if !a.parkedInAir("Chochok", "-446.5,81.6,-156.0", t0.Add(13*time.Second)) {
		t.Fatal("not parked after 13 s in one place")
	}
	// Flies off: moving again, not parked, and the clock starts over.
	if a.parkedInAir("Chochok", "-430.0,85.0,-150.0", t0.Add(14*time.Second)) {
		t.Fatal("parked while moving")
	}
	if a.parkedInAir("Chochok", "-430.5,85.0,-150.0", t0.Add(20*time.Second)) {
		t.Fatal("parked 6 s after moving")
	}
	if a.parkedInAir("x", "garbage", t0.Add(time.Hour)) {
		t.Fatal("parked on a bad position")
	}
}
