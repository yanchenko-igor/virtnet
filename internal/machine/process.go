package machine

import (
	"bytes"
	"time"
)

// ProcessState is a virtual process's lifecycle state.
type ProcessState int

// Lifecycle states. A process is a cooperative object, never a host process
// (ARCHITECTURE.md §7.7).
const (
	Running ProcessState = iota
	Waiting              // paused until data arrives or its wakeup deadline passes
	Exited
)

// String returns the state name.
func (s ProcessState) String() string {
	switch s {
	case Running:
		return "RUNNING"
	case Waiting:
		return "WAITING"
	default:
		return "EXITED"
	}
}

// Process is a cooperative virtual process: a pid, standard streams, a lazy
// wakeup deadline in virtual time, and a step hook. Nothing blocks the host
// thread; the machine drives processes when the shell/UI steps the machine.
type Process struct {
	Pid      int
	Name     string
	Args     []string
	State    ProcessState
	Stdin    *bytes.Buffer
	Stdout   *BoundedBuffer
	Stderr   *BoundedBuffer
	WakeupAt time.Duration // virtual-clock deadline; zero = no time-based wakeup
	ExitCode int

	m    *Machine
	step func(m *Machine, p *Process)

	// Data carries app-specific state between steps (e.g. a listening socket).
	Data any
}

// BoundedBuffer is a bytes.Buffer with a maximum size. When full, new writes
// evict the oldest data (like a ring buffer for scrollback).
type BoundedBuffer struct {
	buf     []byte
	maxSize int
}

// NewBoundedBuffer creates a buffer with the given max size (e.g. 64KB).
func NewBoundedBuffer(maxSize int) *BoundedBuffer {
	return &BoundedBuffer{buf: make([]byte, 0, maxSize), maxSize: maxSize}
}

// WriteString appends s to the buffer, evicting oldest data if needed.
func (b *BoundedBuffer) WriteString(s string) (int, error) {
	data := []byte(s)
	if len(data) >= b.maxSize {
		// Single write exceeds limit: keep only the suffix.
		b.buf = data[len(data)-b.maxSize:]
		return len(data), nil
	}
	if len(b.buf)+len(data) > b.maxSize {
		// Evict oldest to make room.
		keep := b.maxSize - len(data)
		b.buf = b.buf[len(b.buf)-keep:]
	}
	b.buf = append(b.buf, data...)
	return len(data), nil
}

// String returns the buffer contents as a string.
func (b *BoundedBuffer) String() string {
	return string(b.buf)
}

// Len returns the current buffer length.
func (b *BoundedBuffer) Len() int {
	return len(b.buf)
}

// Reset clears the buffer.
func (b *BoundedBuffer) Reset() {
	b.buf = b.buf[:0]
}

// newProcess creates a process with empty standard streams.
func newProcess(m *Machine, name string, args []string) *Process {
	return &Process{
		Pid:    m.nextPID,
		Name:   name,
		Args:   args,
		State:  Running,
		Stdin:  &bytes.Buffer{},
		Stdout: NewBoundedBuffer(MaxTranscriptBytes),
		Stderr: NewBoundedBuffer(MaxTranscriptBytes),
		m:      m,
	}
}

// writeOut appends text to the process's stdout.
func (p *Process) writeOut(s string) {
	_, _ = p.Stdout.WriteString(s)
}

// writeErr appends text to the process's stderr.
func (p *Process) writeErr(s string) {
	_, _ = p.Stderr.WriteString(s)
}

// exit marks the process as exited with the given code.
func (p *Process) exit(code int) {
	p.State = Exited
	p.ExitCode = code
}

// sleep sets the lazy wakeup deadline: duration in virtual time from now. The
// process is not scheduled; whoever next steps the machine compares the
// deadline against the virtual clock (ARCHITECTURE.md §5.4).
func (p *Process) sleep(d time.Duration) {
	p.WakeupAt = p.m.clock.Now() + d
}

// wakeupDue reports whether the virtual clock has passed the deadline.
func (p *Process) wakeupDue(now time.Duration) bool {
	return p.WakeupAt != 0 && now >= p.WakeupAt
}

// waitForData pauses the process until it is stepped again (data or wakeup).
func (p *Process) waitForData() {
	p.State = Waiting
}
