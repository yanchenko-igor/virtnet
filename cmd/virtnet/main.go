// Command virtnet runs the single-process virtual network simulator.
//
// Phase 1: a minimal seed-driven determinism demo. The TUI arrives in phase 7.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yanchenko-igor/virtnet/internal/sim"
)

func main() {
	seed := flag.Uint64("seed", 12345, "simulation randomness seed")
	flag.Parse()

	s := sim.New(*seed)
	if err := s.Clock().AdvanceBy(100 * time.Millisecond); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	digest, err := s.Digest()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("seed:         %d\n", *seed)
	fmt.Printf("virtual time: %v\n", s.Clock().Now())
	fmt.Printf("state digest: %x\n", digest)
	fmt.Println("same seed → same digest (determinism)")
}
