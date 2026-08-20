package netstack

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/clock"
	"github.com/yanchenko-igor/virtnet/internal/fabric"
)

// TestStackStateJSONRoundTrip verifies that a stack's serializable state
// survives a JSON round trip byte-for-byte and that the restored stack
// behaves identically: the listener still hands out its backlog child with
// the queued data, and the UDP queue is intact.
func TestStackStateJSONRoundTrip(t *testing.T) {
	c, pc1, pc2 := setupPair(t, 10*time.Millisecond)

	l, err := pc1.Listen(7000)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	conn, err := pc2.Dial(mustAddr(t, "10.0.0.10"), 7000)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(l.backlog) != 1 {
		t.Fatalf("backlog = %d, want 1 (handshake completes inside Dial)", len(l.backlog))
	}

	pc2u := pc2.NewUDPSocket()
	if err := pc2u.Bind(9000); err != nil {
		t.Fatalf("pc2 UDP bind: %v", err)
	}
	recv := pc1.NewUDPSocket()
	if err := recv.Bind(9000); err != nil {
		t.Fatalf("pc1 UDP bind: %v", err)
	}
	if err := pc2u.SendTo(mustAddr(t, "10.0.0.10"), 9000, []byte("pingdata")); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if len(recv.rxq) != 1 {
		t.Fatalf("pc1 UDP rxq = %d, want 1", len(recv.rxq))
	}

	before, err := json.Marshal(pc1.State())
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	c2 := clock.New()
	c2.Set(c.Now())
	iface := fabric.NewInterface("eth0", mustMAC(t, "02:00:00:00:00:01"))
	var st StackState
	if err := json.Unmarshal(before, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	pc1r, err := RestoreStack(c2, iface, st)
	if err != nil {
		t.Fatalf("RestoreStack: %v", err)
	}

	after, err := json.Marshal(pc1r.State())
	if err != nil {
		t.Fatalf("marshal restored state: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("restored stack state diverged:\nbefore: %s\nafter:  %s", before, after)
	}

	// Behavior: the restored listener hands out its backlog child with data.
	lr := pc1r.Listener(7000)
	if lr == nil {
		t.Fatal("restored listener missing")
	}
	child, err := lr.Accept()
	if err != nil {
		t.Fatalf("restored Accept: %v", err)
	}
	buf := make([]byte, 16)
	n, err := child.Read(buf)
	if err != nil || n != 5 || string(buf[:n]) != "hello" {
		t.Fatalf("restored child read = %q (n=%d, err=%v), want hello", buf[:n], n, err)
	}

	// Behavior: the restored UDP socket still delivers its queued datagram.
	ur := pc1r.udpSockets[9000]
	if ur == nil {
		t.Fatal("restored UDP socket missing")
	}
	_, _, data, ok := ur.RecvFrom()
	if !ok || string(data) != "pingdata" {
		t.Fatalf("restored UDP read = %q (ok=%v), want pingdata", data, ok)
	}
}

// TestStackStateDeterministic asserts that State() emits identical bytes on
// repeated calls for the same stack (no map-order leaks).
func TestStackStateDeterministic(t *testing.T) {
	_, pc1, pc2 := setupPair(t, 10*time.Millisecond)
	if _, err := pc1.Listen(7000); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if _, err := pc2.Dial(mustAddr(t, "10.0.0.10"), 7000); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	a, _ := json.Marshal(pc1.State())
	b, _ := json.Marshal(pc1.State())
	if string(a) != string(b) {
		t.Fatal("Stack.State() is not deterministic")
	}
}
