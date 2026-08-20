package machine

import "fmt"

// MaxTranscriptBytes is the maximum transcript size retained. Older output
// is dropped to bound memory usage (prevents OOM from commands like
// `ping -c 1000000`).
const MaxTranscriptBytes = 64 * 1024

// Console is a machine's transcript (ARCHITECTURE.md §7.2). The UI (phase 7)
// attaches a terminal to it; the machine itself never depends on the UI.
type Console struct {
	prompt     string
	transcript []byte
}

// NewConsole returns a console whose prompts read like "pc1$ ".
func NewConsole(prompt string) *Console {
	return &Console{prompt: prompt}
}

// Prompt returns the console's prompt string.
func (c *Console) Prompt() string {
	return c.prompt
}

// Write appends raw text to the transcript, evicting oldest data if needed.
func (c *Console) Write(s string) {
	b := []byte(s)
	if len(b) >= MaxTranscriptBytes {
		// Single write exceeds limit: keep only the suffix.
		c.transcript = b[len(b)-MaxTranscriptBytes:]
		return
	}
	if len(c.transcript)+len(b) > MaxTranscriptBytes {
		// Evict oldest to make room.
		keep := MaxTranscriptBytes - len(b)
		c.transcript = c.transcript[len(c.transcript)-keep:]
	}
	c.transcript = append(c.transcript, b...)
}

// Writef appends formatted text to the transcript.
func (c *Console) Writef(format string, args ...any) {
	c.Write(fmt.Sprintf(format, args...))
}

// WritePrompt writes the prompt to the transcript.
func (c *Console) WritePrompt() {
	c.Write(c.prompt)
}

// Transcript returns the full session output recorded so far (bounded by
// MaxTranscriptBytes).
func (c *Console) Transcript() string {
	return string(c.transcript)
}

// Reset clears the transcript.
func (c *Console) Reset() {
	c.transcript = nil
}
