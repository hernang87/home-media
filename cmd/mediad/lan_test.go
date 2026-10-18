package main

import (
	"context"
	"testing"
	"time"
)

func TestWatchLANReportsChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrs := make(chan string, 4)
	for _, a := range []string{"10.0.0.2", "10.0.0.2", "10.0.0.7"} {
		addrs <- a
	}
	lookup := func() string {
		select {
		case a := <-addrs:
			return a
		default:
			return "10.0.0.7"
		}
	}
	got := make(chan string, 4)
	go watchLAN(ctx, 8200, time.Millisecond, "10.0.0.2", lookup, func(_, b string) { got <- b })
	select {
	case b := <-got:
		if b != "http://10.0.0.7:8200" {
			t.Errorf("base = %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change reported")
	}
	select {
	case b := <-got:
		t.Errorf("unexpected second report %q", b)
	case <-time.After(50 * time.Millisecond):
	}
}
