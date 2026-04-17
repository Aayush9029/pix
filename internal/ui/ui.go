package ui

import (
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	Green   = "\033[32m"
	Red     = "\033[31m"
	Yellow  = "\033[33m"
	Cyan    = "\033[36m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Orange  = "\033[38;5;208m"
	Dim     = "\033[2m"
	Bold    = "\033[1m"
	Reset   = "\033[0m"
)

// variantPalette assigns each variant a stable color so its frame logs and
// its final-success line share the same hue — easy to correlate at a glance.
var variantPalette = []string{Cyan, Magenta, Yellow, Green, Blue, Orange, Red}

// VariantColor returns the ANSI color for variant index i (0-based).
func VariantColor(i int) string {
	return variantPalette[i%len(variantPalette)]
}

// LiveBadge returns a bold red "LIVE" label for streaming output headers.
func LiveBadge() string {
	return Bold + Red + "LIVE" + Reset
}

func IsTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func StderrIsTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func Header(msg string) {
	if !IsTTY() {
		return
	}
	fmt.Printf("%s%s⚡ %s%s\n", Cyan, Bold, msg, Reset)
}

func Success(msg string) {
	fmt.Printf("%s✓ %s%s\n", Green, msg, Reset)
}

func Error(msg string) {
	fmt.Fprintf(os.Stderr, "%s✗ %s%s\n", Red, msg, Reset)
}

func Status(msg string) {
	fmt.Printf("%s→ %s%s\n", Dim, msg, Reset)
}

func Dimf(format string, a ...any) {
	fmt.Printf("%s"+format+"%s\n", append([]any{Dim}, append(a, Reset)...)...)
}

func Fatalf(format string, a ...any) {
	Error(fmt.Sprintf(format, a...))
	os.Exit(1)
}

// Spinner renders a tiny spinner on stderr until Stop is called.
type Spinner struct {
	label  string
	stop   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	active bool
}

func NewSpinner(label string) *Spinner {
	return &Spinner{label: label, stop: make(chan struct{}), done: make(chan struct{})}
}

func (s *Spinner) Start() {
	if !StderrIsTTY() {
		return
	}
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()

	go func() {
		defer close(s.done)
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		i := 0
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				fmt.Fprint(os.Stderr, "\r\033[K")
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, "\r%s%s%s %s ", Cyan, frames[i%len(frames)], Reset, s.label)
				i++
			}
		}
	}()
}

func (s *Spinner) Update(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

func (s *Spinner) Stop() {
	s.mu.Lock()
	active := s.active
	s.active = false
	s.mu.Unlock()
	if !active {
		return
	}
	close(s.stop)
	<-s.done
}
