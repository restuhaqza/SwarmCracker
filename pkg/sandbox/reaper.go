package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// The reaper: what takes an idle sandbox back.
//
// A sandbox is a machine someone asked for and may have walked away from. Nothing in
// the guest tells this node whether its work is done, and a sandbox that is never
// used again would otherwise hold a VM, a namespace, a disk and a cgroup for as long
// as the node runs. So every sandbox may carry an idle TTL (Record.IdleTTLSeconds),
// and the reaper is the node's answer to what happens when it expires: the sandbox is
// removed exactly as `rm` removes it, and nothing of it is left.
//
// # Idle means nobody asked it to do anything
//
// The clock starts at LastActive, which moves when the node creates the sandbox and
// on every exec into it (Record.IdleFor explains why nothing else can be observed).
// A sandbox whose guest is busy inside itself, with no new exec, is idle by this
// definition — and that is the honest definition, because the node has no way to know
// otherwise. A caller that wants a long-running sandbox to survive sets a TTL that
// covers its gaps, or execs in it.
//
// # One rule for every state
//
// A ready sandbox past its TTL, a create that died in state created, and a failed
// record left behind all reach the reaper the same way: their last activity is older
// than their TTL, so they are removed. There is no separate garbage collection for
// failed records, because a second age rule would be a second thing to get wrong.

// Reaped is one sandbox the reaper took back, and what that freed.
type Reaped struct {
	// ID is the sandbox.
	ID string
	// State is the state it was in when the reaper found it.
	State State
	// IdleFor is how long it had been unused when it was taken.
	IdleFor time.Duration
	// Freed is what the removal freed. It is zero when Err is set.
	Freed Freed
	// Err is why this sandbox could not be reaped. The others still are: one
	// sandbox that cannot be removed must not keep every idle sandbox alive.
	Err error
}

// Reap takes back every sandbox that has been idle past its own TTL at now, and
// reports what it removed. It returns an error only when the node's records cannot be
// read at all; a sandbox that could not be removed is reported in its Reaped entry,
// so one stuck sandbox does not hide the others.
//
// It is the one-shot form the CLI runs and the loop below repeats; they are the same
// decision, so a node that reaps by hand and a node that reaps on a timer cannot
// disagree about what idle means.
func (m *Manager) Reap(ctx context.Context, now time.Time) ([]Reaped, error) {
	records, skipped, err := m.cfg.Store.List()
	if err != nil {
		return nil, err
	}
	for _, path := range skipped {
		log.Warn().Str("record", path).Msg("Skipping a sandbox record that cannot be read; the reaper cannot act on it")
	}

	var reaped []Reaped
	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return reaped, err
		}
		if !rec.Idle(now) {
			continue
		}

		entry := Reaped{ID: rec.ID, State: rec.State, IdleFor: rec.IdleFor(now)}
		freed, err := m.Remove(ctx, rec.ID)
		if err != nil {
			entry.Err = err
			log.Warn().Err(err).Str("sandbox_id", rec.ID).
				Msg("Could not reap an idle sandbox; it stays for the next pass")
		} else {
			entry.Freed = freed
			log.Info().
				Str("sandbox_id", rec.ID).
				Str("state", string(rec.State)).
				Dur("idle_for", entry.IdleFor).
				Dur("ttl", rec.TTL()).
				Msg("Reaped an idle sandbox")
		}
		reaped = append(reaped, entry)
	}

	return reaped, nil
}

// ReapEvery reaps once per interval until ctx is done. It is the daemon's shape of
// the reaper: a sandbox that idles is taken back within an interval of its TTL, and
// the loop stops with the daemon instead of outliving it.
//
// The first pass runs immediately, so a daemon that starts on a node whose previous
// life left idle sandboxes does not wait an interval to collect them.
func (m *Manager) ReapEvery(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("sandbox: reap interval %s is not positive", interval)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := m.Reap(ctx, m.now()); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn().Err(err).Msg("Could not reap idle sandboxes; the next pass will try again")
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
