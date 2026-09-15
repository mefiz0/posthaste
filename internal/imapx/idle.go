package imapx

import (
	"context"
	"fmt"
	"sync"

	"github.com/emersion/go-imap/v2"
)

// Idle parks the connection in IMAP IDLE on path and blocks until the server
// reports new mail (a nudge, in which case it returns nil), until ctx is
// cancelled (in which case it returns the context error), or until the session
// ends unexpectedly (returned as an error). The go-imap client re-issues the
// IDLE command itself roughly every 28 minutes, so no local re-issue timer is
// needed; the sync worker simply loops sync, Idle, repeat.
func (c *Client) Idle(ctx context.Context, path string) error {
	end := c.beginOp(ctx)
	if err := c.selectForOperation(path, true); err != nil {
		end()
		return err
	}
	end()

	// Stale nudges from earlier unilateral reports would end a fresh IDLE
	// immediately; only signals arriving while parked matter.
	for {
		select {
		case <-c.nudges:
			continue
		default:
		}
		break
	}

	idleCmd, err := c.client.Idle()
	if err != nil {
		return fmt.Errorf("imapx: start idle on %s: %w", path, err)
	}

	// Exactly one closer: IDLE ends with DONE from whichever fires first,
	// the nudge channel or the context.
	var once sync.Once
	stop := func() {
		once.Do(func() { _ = idleCmd.Close() })
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- idleCmd.Wait() }()

	// No command deadline applies here: parking for up to ~28 minutes per
	// IDLE round is the whole point, so only ctx can cut it short.
	stopWatch := c.watchCancel(ctx)
	defer stopWatch()

	select {
	case err := <-waitErr:
		stop()
		if err != nil {
			return fmt.Errorf("imapx: idle on %s: %w", path, err)
		}
		return nil
	case <-c.nudges:
		stop()
		<-waitErr
		return nil
	case <-ctx.Done():
		stop()
		<-waitErr
		return fmt.Errorf("imapx: idle on %s: %w", path, ctx.Err())
	}
}

// SupportsIdle reports whether the server advertises the IDLE extension.
func (c *Client) SupportsIdle(ctx context.Context) (bool, error) {
	end := c.beginOp(ctx)
	defer end()

	caps, err := c.capabilities()
	if err != nil {
		return false, err
	}
	return caps.Has(imap.CapIdle), nil
}
