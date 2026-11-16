package analysis

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Phases records how long the named stages of one job took. It carries no
// values or identifiers, only stage names and durations, so it can be logged.
type Phases struct {
	mu    sync.Mutex
	last  time.Time
	names []string
	spans []time.Duration
}

type phasesKey struct{}

// WithPhases attaches a fresh stage recorder to ctx.
func WithPhases(ctx context.Context) (context.Context, *Phases) {
	p := &Phases{last: time.Now()}
	return context.WithValue(ctx, phasesKey{}, p), p
}

// Mark closes the stage that ran since the previous mark (or the start). It is
// a no-op without a recorder.
func Mark(ctx context.Context, name string) {
	p, _ := ctx.Value(phasesKey{}).(*Phases)
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for i, n := range p.names {
		if n == name {
			p.spans[i] += now.Sub(p.last)
			p.last = now
			return
		}
	}
	p.names = append(p.names, name)
	p.spans = append(p.spans, now.Sub(p.last))
	p.last = now
}

// String renders "name=ms,name=ms" in first-seen order.
func (p *Phases) String() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var b strings.Builder
	for i, n := range p.names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteByte('=')
		b.WriteString(formatMS(p.spans[i]))
	}
	return b.String()
}

// Durations returns a copy of the recorded stages.
func (p *Phases) Durations() map[string]time.Duration {
	out := map[string]time.Duration{}
	if p == nil {
		return out
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, n := range p.names {
		out[n] = p.spans[i]
	}
	return out
}

func formatMS(d time.Duration) string {
	ms := d.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	var buf [20]byte
	i := len(buf)
	if ms == 0 {
		return "0"
	}
	for ms > 0 {
		i--
		buf[i] = byte('0' + ms%10)
		ms /= 10
	}
	return string(buf[i:])
}
