package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mefiz0/posthaste/internal/store"
)

// errVanished marks a queued action whose target no longer exists on the
// server, so replay must drop it instead of retrying it.
var errVanished = errors.New("sync: action target no longer exists")

// droppedActionNotice is the generic user-facing text for a discarded offline
// change. It deliberately carries no message details.
const droppedActionNotice = "An offline change was discarded because the message changed on the server"

// vanishedMarkers are the phrases IMAP servers use when a message or mailbox
// an action targets is gone. Matching is a last resort for errors that arrive
// without a structured response code.
var vanishedMarkers = []string{
	"no such message",
	"nosuchmessage",
	"no such mailbox",
	"nosuchmailbox",
	"does not exist",
	"nonexistent",
	"not found",
	"trycreate",
}

// replayQueue applies the durable offline queue against the server, oldest
// first. Applied and dropped actions are removed; a transient failure records
// the attempt and aborts the session so the queue keeps its order for the
// reconnect that follows.
func (w *Worker) replayQueue(ctx context.Context, server MailServer) error {
	for {
		actions, err := w.deps.DB.ListActions(ctx, 0)
		if err != nil {
			return err
		}
		if len(actions) == 0 {
			return nil
		}
		progressed := false
		for _, action := range actions {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := w.applyAction(ctx, server, action)
			switch {
			case err == nil:
				if err := w.deps.DB.DeleteAction(ctx, action.ID); err != nil {
					return err
				}
				progressed = true
			case isVanishedError(err):
				// Server state won: the message or destination is gone, so
				// the offline change no longer has a target. Drop it and tell
				// the user once, without any message detail.
				if err := w.deps.DB.DeleteAction(ctx, action.ID); err != nil {
					return err
				}
				progressed = true
				w.emit(Event{State: StateSyncing, Notice: droppedActionNotice})
			default:
				if recordErr := w.deps.DB.RecordActionAttempt(ctx, action.ID, scrubErrText(err)); recordErr != nil {
					return recordErr
				}
				// Reconnect and retry from the front of the queue: stopping
				// here preserves replay order.
				return fmt.Errorf("sync: replay action %d (%s): %w", action.ID, action.Kind, err)
			}
		}
		if !progressed {
			return errors.New("sync: action queue made no progress")
		}
	}
}

// applyAction pushes one queued change to the server. A missing source or
// target folder, or an unknown action kind, reports errVanished so the action
// is dropped rather than retried forever.
func (w *Worker) applyAction(ctx context.Context, server MailServer, action store.Action) error {
	folder, err := w.deps.DB.FolderByID(ctx, action.FolderID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errVanished
		}
		return err
	}

	switch action.Kind {
	case store.ActionFlag, store.ActionUnflag:
		add := flagNames(action.AddFlags)
		remove := flagNames(action.RemoveFlags)
		if len(add) == 0 && len(remove) == 0 {
			return nil
		}
		return server.SetFlags(ctx, folder.IMAPPath, action.UID, add, remove)
	case store.ActionRead:
		return server.SetFlags(ctx, folder.IMAPPath, action.UID, []string{seenFlagName}, nil)
	case store.ActionUnread:
		return server.SetFlags(ctx, folder.IMAPPath, action.UID, nil, []string{seenFlagName})
	case store.ActionMove, store.ActionArchive:
		target, err := w.deps.DB.FolderByID(ctx, action.TargetFolderID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errVanished
			}
			return err
		}
		return server.Move(ctx, folder.IMAPPath, action.UID, target.IMAPPath)
	case store.ActionDelete:
		return server.Delete(ctx, folder.IMAPPath, action.UID)
	default:
		// An unknown kind can never be executed; keeping it would block the
		// queue forever, so it is dropped the same way as a vanished target.
		return errVanished
	}
}

// isVanishedError reports whether an action failed because its target is
// gone: either the structured sentinel or a server error whose text matches a
// known vanished phrase.
func isVanishedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errVanished) {
		return true
	}
	flat := flattenErrText(err)
	for _, marker := range vanishedMarkers {
		if strings.Contains(flat, marker) {
			return true
		}
	}
	return false
}
