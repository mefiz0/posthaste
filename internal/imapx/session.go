package imapx

import (
	"context"
	"sync"
	"time"
)

// beginOp bounds one IMAP operation on the connection. The socket gets a
// read/write deadline (the context deadline if sooner, otherwise a default)
// so a silent server cannot stall a worker forever, and context cancellation
// interrupts any in-flight command by closing the connection. The returned
// end function must be called when the operation finishes; it releases the
// cancellation watcher and clears the deadline so a later IDLE can park
// indefinitely.
//
// Note that a triggered deadline or cancellation is terminal for the
// connection: the caller is expected to discard the Client and reconnect.
func (c *Client) beginOp(ctx context.Context) (end func()) {
	deadline := time.Now().Add(commandTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetDeadline(deadline)
	stop := c.watchCancel(ctx)
	var once sync.Once
	return func() {
		once.Do(func() {
			stop()
			_ = c.conn.SetDeadline(time.Time{})
		})
	}
}

// watchCancel closes the connection when ctx is cancelled, unblocking any
// command that is waiting on the server. The returned stop function releases
// the watcher without closing anything.
func (c *Client) watchCancel(ctx context.Context) (stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.conn.Close()
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
	}
}
