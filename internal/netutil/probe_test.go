package netutil

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestProbeTCPReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	got, err := ProbeTCP(context.Background(), ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("ProbeTCP: %v", err)
	}
	if got != Reachable {
		t.Errorf("got %v, want Reachable", got)
	}
}

// TestProbeTCPRefusedIsUnreachable: a refused connection is positive evidence
// that nothing is listening, which is what the executor may act on.
func TestProbeTCPRefusedIsUnreachable(t *testing.T) {
	// Bind then immediately close, so the port is almost certainly free and
	// the kernel will refuse.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	got, err := ProbeTCP(context.Background(), addr, time.Second)
	if err == nil {
		t.Skip("port was reused by another process; cannot test refusal")
	}
	if got != Unreachable {
		t.Errorf("got %v for a refused connection, want Unreachable (err: %v)", got, err)
	}
}

// TestProbeTCPTimeoutIsIndeterminate is the regression test for the central
// design flaw: every transport reported a dial failure as StateDown, so a
// network partition to a running host read as "shutdown complete".
//
// 203.0.113.0/24 is TEST-NET-3 (RFC 5737); it is reserved for documentation
// and is not routable, so a connection attempt hangs until the timeout.
func TestProbeTCPTimeoutIsIndeterminate(t *testing.T) {
	got, err := ProbeTCP(context.Background(), "203.0.113.1:22", 100*time.Millisecond)
	if err == nil {
		t.Skip("unexpected connectivity to TEST-NET-3")
	}
	if got != Indeterminate {
		t.Errorf("got %v for a timeout, want Indeterminate — a partition would "+
			"be mistaken for a completed shutdown (err: %v)", got, err)
	}
}

func TestProbeTCPDNSFailureIsIndeterminate(t *testing.T) {
	got, err := ProbeTCP(context.Background(),
		"this-host-does-not-exist.invalid:22", time.Second)
	if err == nil {
		t.Skip("resolver returned an address for a .invalid name")
	}
	if got != Indeterminate {
		t.Errorf("got %v for a DNS failure, want Indeterminate (err: %v)", got, err)
	}
}

func TestProbeTCPCancelledContextIsIndeterminate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := ProbeTCP(ctx, "203.0.113.1:22", time.Second)
	if err == nil {
		t.Skip("dial succeeded despite a cancelled context")
	}
	if got != Indeterminate {
		t.Errorf("got %v for a cancelled context, want Indeterminate", got)
	}
}

func TestClassifyDialError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Reachability
	}{
		{"nil", nil, Reachable},
		{"refused", syscall.ECONNREFUSED, Unreachable},
		{"host unreachable", syscall.EHOSTUNREACH, Unreachable},
		{"network unreachable", syscall.ENETUNREACH, Indeterminate},
		{"timeout", context.DeadlineExceeded, Indeterminate},
		{"cancelled", context.Canceled, Indeterminate},
		{"unknown", errors.New("something else"), Indeterminate},
		{"wrapped refused", &net.OpError{Err: syscall.ECONNREFUSED}, Unreachable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyDialError(tt.err); got != tt.want {
				t.Errorf("ClassifyDialError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestReachabilityString(t *testing.T) {
	for _, tt := range []struct {
		r    Reachability
		want string
	}{
		{Reachable, "reachable"},
		{Unreachable, "unreachable"},
		{Indeterminate, "indeterminate"},
	} {
		if got := tt.r.String(); got != tt.want {
			t.Errorf("Reachability(%d).String() = %q, want %q", tt.r, got, tt.want)
		}
	}
}
