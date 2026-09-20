package netutil

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"
)

// Reachability is what a network probe could establish about a host.
//
// The three-way distinction matters because Canarium's job is deciding when
// a host is safely off. "I could not reach it" is not evidence that a host is
// down — it is equally consistent with a switch reboot, a VLAN change, or a
// dropped uplink while the host runs happily on.
type Reachability int

const (
	// Indeterminate means the probe established nothing. The network path
	// failed before any conclusion could be drawn.
	Indeterminate Reachability = iota

	// Reachable means the host answered.
	Reachable

	// Unreachable means the host actively refused, or the network reported
	// that it does not exist. This is positive evidence of absence.
	Unreachable
)

func (r Reachability) String() string {
	switch r {
	case Reachable:
		return "reachable"
	case Unreachable:
		return "unreachable"
	default:
		return "indeterminate"
	}
}

// ProbeTCP attempts a TCP connection and classifies the outcome.
//
// The classification is the point of this function:
//
//   - A successful connection means the host is up.
//
//   - ECONNREFUSED means something answered on the network path and said
//     "nothing is listening here". On a host that has shut down its services
//     but not yet powered off, or one whose IP has been taken over, that is
//     meaningful. EHOSTUNREACH likewise means a router on the path actively
//     reported the host absent.
//
//   - A timeout, a DNS failure, ENETUNREACH or a context cancellation
//     establish nothing at all. Reporting these as "down" is how a network
//     partition gets mistaken for a completed shutdown.
func ProbeTCP(ctx context.Context, addr string, timeout time.Duration) (Reachability, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err == nil {
		conn.Close()
		return Reachable, nil
	}

	return ClassifyDialError(err), err
}

// ClassifyDialError maps a dial error to what it proves about the host.
func ClassifyDialError(err error) Reachability {
	if err == nil {
		return Reachable
	}

	// Refused, or a router reporting the host absent: positive evidence.
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) {
		return Unreachable
	}

	// Everything else — timeouts, DNS failures, ENETUNREACH, cancellation —
	// tells us about the network between here and there, not about the host.
	return Indeterminate
}
