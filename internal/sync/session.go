package sync

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/mefiz0/posthaste/internal/store"
)

// bodyBatchSize bounds how many full messages are requested per fetch round
// trip so one pass over a large backlog transfers in steady chunks instead of
// one huge response.
const bodyBatchSize = 10

// maxPendingPerPass bounds how many half-ingested rows are completed per
// folder pass; the rest are picked up on the following pass, so a crash
// cannot turn into one enormous catch-up fetch.
const maxPendingPerPass = 200

// listPage is the page size for scanning local folders.
const listPage = 200

// wakeReason says why a park window ended.
type wakeReason int

// Park wake reasons.
const (
	wakeNudge   wakeReason = iota // IDLE reported activity on the parked folder
	wakePoll                      // poll ticker fired
	wakeTrigger                   // explicit TriggerSync
	wakeAppend                    // an APPEND request was queued
)

// bodyJob is a message row waiting for its body to be fetched and ingested.
type bodyJob struct {
	rowID int64
	uid   uint32
}

// runSession owns one live connection: it replays queued actions, reconciles
// folders, runs a full pass over every folder, and then parks in IDLE or on
// the poll ticker, running light passes when woken. It returns only when the
// connection is no longer usable (or ctx ends); the caller closes the server.
func (w *Worker) runSession(ctx context.Context, server MailServer) error {
	if err := w.replayQueue(ctx, server); err != nil {
		return err
	}
	folders, err := w.reconcileFolders(ctx, server)
	if err != nil {
		return err
	}
	if err := w.syncFolders(ctx, server, folders); err != nil {
		return err
	}

	idleSupported, err := server.SupportsIdle(ctx)
	if err != nil {
		return err
	}
	var ticker *time.Ticker
	var tickC <-chan time.Time
	if !idleSupported {
		// The IMAP layer re-issues IDLE itself, so only the polling fallback
		// needs a local timer.
		ticker = time.NewTicker(w.pollInterval())
		defer ticker.Stop()
		tickC = ticker.C
	}
	inbox := inboxPath(folders)

	for {
		w.emit(Event{State: StateIdle})
		wake, err := w.park(ctx, server, inbox, idleSupported, tickC)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		w.emit(Event{State: StateSyncing})

		// Queued actions own their messages' state, so anything the user
		// changed while this connection was parked goes to the server before
		// a flag sync could fight the optimistic local edits.
		if err := w.replayQueue(ctx, server); err != nil {
			return err
		}

		pass := folders
		switch wake {
		case wakeNudge:
			// An IDLE nudge only reports activity on the parked folder.
			pass = foldersWhere(folders, isInbox)
		case wakeAppend:
			// The APPEND was already served by park; its folder is ingested
			// by the targeted pass below, so no full pass is needed.
			pass = nil
		case wakeTrigger:
			// A manual refresh asks for every folder; an action confined to
			// one folder asks for just that one.
			full, targets := w.takeTrigger()
			if !full {
				pass = foldersWhere(folders, func(f store.Folder) bool {
					return targets[f.IMAPPath]
				})
			}
		}
		if err := w.syncFolders(ctx, server, pass); err != nil {
			return err
		}
		// Appends served during the pass (or just now) need their uploaded
		// copy ingested. Syncing only those folders keeps a sent message
		// visible promptly without scanning every mailbox.
		if err := w.syncAppendedFolders(ctx, server, folders); err != nil {
			return err
		}
	}
}

// syncAppendedFolders runs a targeted pass over the folders that received an
// APPEND since the last check, draining any appends that arrived meanwhile.
func (w *Worker) syncAppendedFolders(ctx context.Context, server MailServer, folders []store.Folder) error {
	pending := w.takePendingAppendFolders()
	if len(pending) == 0 {
		return nil
	}
	// Serve anything that queued while the previous pass was running, so the
	// caller's AppendRaw does not wait for the next park window.
	w.drainAppends(server)
	for path := range w.takePendingAppendFolders() {
		pending[path] = true
	}
	return w.syncFolders(ctx, server, foldersWhere(folders, func(f store.Folder) bool {
		return pending[f.IMAPPath]
	}))
}

// park waits until there is something to do. With IDLE the connection is
// parked through a short-lived watcher goroutine that is always joined before
// park returns, because APPEND cannot run while the connection is parked.
func (w *Worker) park(ctx context.Context, server MailServer, inbox string, idleSupported bool, tickC <-chan time.Time) (wakeReason, error) {
	if idleSupported && inbox != "" {
		idleCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		idleDone := make(chan error, 1)
		go func() {
			// Owned by this park window: cancel above always runs before park
			// returns, and Idle returns once its context is cancelled.
			idleDone <- server.Idle(idleCtx, inbox)
		}()

		select {
		case err := <-idleDone:
			// nil means the server nudged; an error means the connection died
			// or the outer context ended, both handled by the caller.
			return wakeNudge, err
		case <-tickC:
			cancel()
			<-idleDone
			return wakePoll, nil
		case <-w.trigger:
			cancel()
			<-idleDone
			return wakeTrigger, nil
		case request := <-w.appends:
			cancel()
			<-idleDone
			w.runAppend(server, request)
			w.drainAppends(server)
			return wakeAppend, nil
		case <-ctx.Done():
			return wakeNudge, ctx.Err()
		}
	}

	select {
	case <-tickC:
		return wakePoll, nil
	case <-w.trigger:
		return wakeTrigger, nil
	case request := <-w.appends:
		w.runAppend(server, request)
		w.drainAppends(server)
		return wakeAppend, nil
	case <-ctx.Done():
		return wakeNudge, ctx.Err()
	}
}

// drainAppends serves every APPEND that queued while the connection was busy.
func (w *Worker) drainAppends(server MailServer) {
	for {
		select {
		case request := <-w.appends:
			w.runAppend(server, request)
		default:
			return
		}
	}
}

// reconcileFolders mirrors the server's mailbox list into the local folders
// table, preserving stored sync cursors, and drops locally known user folders
// that vanished server-side. Well-known folders are kept even when the server
// stops listing them, because account settings point at them by type. It
// returns the folders in sync order, inbox first.
func (w *Worker) reconcileFolders(ctx context.Context, server MailServer) ([]store.Folder, error) {
	remote, err := server.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	local, err := w.deps.DB.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	localByPath := make(map[string]store.Folder, len(local))
	for _, f := range local {
		localByPath[f.IMAPPath] = f
	}

	seen := make(map[string]bool, len(remote))
	for _, rf := range remote {
		seen[rf.Path] = true
		f := store.Folder{
			Name:      folderDisplayName(rf),
			IMAPPath:  rf.Path,
			Type:      folderType(rf.Role),
			Delimiter: rf.Delimiter,
		}
		if prev, ok := localByPath[rf.Path]; ok {
			// The folder upsert rewrites every column, so the stored sync
			// cursor must be carried through or it would reset every pass.
			f.ID = prev.ID
			f.UIDValidity = prev.UIDValidity
			f.UIDNext = prev.UIDNext
			f.TotalCount = prev.TotalCount
			f.UnreadCount = prev.UnreadCount
		}
		if _, err := w.deps.DB.UpsertFolder(ctx, f); err != nil {
			return nil, err
		}
	}
	for _, f := range local {
		if seen[f.IMAPPath] || f.Type != store.FolderUser {
			continue
		}
		if err := w.deps.DB.DeleteFolder(ctx, f.ID); err != nil {
			return nil, err
		}
	}

	fresh, err := w.deps.DB.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	return orderFoldersForSync(fresh), nil
}

// syncFolders runs a pass over each folder in order, serving any APPEND that
// queues between folders so uploads do not stall behind a long pass.
//
// The pass is split deliberately: every folder's headers, flags, and
// existence are reconciled first, then message bodies are downloaded. A slow
// or timing-out body transfer must not stop another folder (Sent, for
// instance) from showing its messages, so body failures are logged and left
// pending for a later pass instead of aborting the pass.
func (w *Worker) syncFolders(ctx context.Context, server MailServer, folders []store.Folder) error {
	if len(folders) == 0 {
		return nil
	}
	w.emitProgress(Progress{Phase: "pass-start", Total: len(folders)})
	type bodyWork struct {
		folder store.Folder
		jobs   []bodyJob
	}
	var pendingBodies []bodyWork
	for _, folder := range folders {
		if err := ctx.Err(); err != nil {
			return err
		}
		jobs, err := w.syncFolder(ctx, server, folder)
		if err != nil {
			return err
		}
		if len(jobs) > 0 {
			pendingBodies = append(pendingBodies, bodyWork{folder: folder, jobs: jobs})
		}
		w.drainAppends(server)
	}
	for _, work := range pendingBodies {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.fetchBodies(ctx, server, work.folder, work.jobs); err != nil {
			if ctx.Err() != nil {
				return err
			}
			// Leave these bodies pending; a later pass completes them.
			w.log("sync: body fetch failed for " + work.folder.Name + ": " + scrubErrText(err))
		}
		w.drainAppends(server)
	}
	w.emitProgress(Progress{Phase: "pass-done", Total: len(folders)})
	return nil
}

// syncFolder reconciles one folder's UIDVALIDITY, headers, flags, and
// existence, and returns the messages whose bodies are not yet stored. The
// caller downloads the bodies afterwards so one slow folder cannot stall the
// rest of the pass.
func (w *Worker) syncFolder(ctx context.Context, server MailServer, folder store.Folder) ([]bodyJob, error) {
	// Re-read so a light pass uses the cursor the previous pass persisted.
	folder, err := w.deps.DB.FolderByID(ctx, folder.ID)
	if err != nil {
		return nil, err
	}

	status, err := server.Select(ctx, folder.IMAPPath)
	if err != nil {
		return nil, err
	}

	cursor := folder.UIDNext
	if status.UIDValidity != folder.UIDValidity {
		if folder.UIDValidity != 0 {
			// The server rebuilt the mailbox, so every stored UID is stale.
			if err := w.wipeFolder(ctx, folder); err != nil {
				return nil, err
			}
		}
		cursor = 1
	}
	if cursor < 1 {
		cursor = 1
	}
	w.emitProgress(Progress{Folder: folder.Name, Phase: "folder-start", Total: status.Total})

	fresh, err := server.FetchHeaders(ctx, folder.IMAPPath, cursor)
	if err != nil {
		return nil, err
	}
	jobs, err := w.insertNewMessages(ctx, folder, fresh)
	if err != nil {
		return nil, err
	}
	pending, err := w.scanPendingBodies(ctx, folder)
	if err != nil {
		return nil, err
	}
	jobs = append(jobs, pending...)

	if err := w.reconcileMessages(ctx, server, folder, fresh, cursor); err != nil {
		return nil, err
	}
	if err := w.deps.DB.UpdateFolderUIDs(ctx, folder.ID, status.UIDValidity, status.UIDNext); err != nil {
		return nil, err
	}
	if err := w.deps.DB.UpdateFolderCounts(ctx, folder.ID, status.Total, status.Unseen); err != nil {
		return nil, err
	}
	w.emitProgress(Progress{Folder: folder.Name, Phase: "folder-done", New: len(fresh), Total: status.Total})
	return jobs, nil
}

// wipeFolder removes every locally stored message of a folder whose UIDVALIDITY
// changed. Attachment rows cascade; unreferenced blobs are reclaimed by the
// periodic garbage collection sweep.
func (w *Worker) wipeFolder(ctx context.Context, folder store.Folder) error {
	messages, err := w.listAllMessages(ctx, folder.ID)
	if err != nil {
		return err
	}
	for _, m := range messages {
		if err := w.deps.DB.DeleteMessage(ctx, m.ID); err != nil {
			return err
		}
	}
	return nil
}

// insertNewMessages stores header rows for messages the folder does not have
// yet and returns their body jobs. Messages already stored (including
// half-ingested ones) are skipped by UID, so passes are idempotent. A server
// message whose row already exists under its Message-ID — most often a
// locally moved row waiting for its server UID — is adopted in place instead
// of being inserted a second time.
func (w *Worker) insertNewMessages(ctx context.Context, folder store.Folder, fresh []ServerMessage) ([]bodyJob, error) {
	var jobs []bodyJob
	for _, sm := range fresh {
		_, err := w.deps.DB.MessageByUID(ctx, folder.ID, sm.UID)
		if err == nil {
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		parsed := parsedHeader(sm.Header)
		existing, adopted, err := w.adoptMatchingRow(ctx, folder, sm, parsed.MessageID)
		if err != nil {
			return nil, err
		}
		if adopted {
			if needsBody(existing) {
				jobs = append(jobs, bodyJob{rowID: existing.ID, uid: sm.UID})
			}
			continue
		}
		rowID, err := w.insertPlaceholder(ctx, folder, sm, parsed)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, bodyJob{rowID: rowID, uid: sm.UID})
	}
	return jobs, nil
}

// scanPendingBodies finds rows whose ingestion never completed: no raw MIME
// stored, or no thread assigned. Re-processing is safe by construction (blob
// writes deduplicate, threading recognizes its own messages, attachment rows
// are only inserted once).
func (w *Worker) scanPendingBodies(ctx context.Context, folder store.Folder) ([]bodyJob, error) {
	var jobs []bodyJob
	for offset := 0; ; offset += listPage {
		messages, err := w.deps.DB.ListMessages(ctx, store.MessageQuery{
			FolderID: folder.ID,
			Limit:    listPage,
			Offset:   offset,
		})
		if err != nil {
			return nil, err
		}
		for _, m := range messages {
			if !needsBody(m) {
				continue
			}
			if m.UID == 0 {
				// A locally moved message is not fetchable by UID; it keeps
				// its body and is reconciled by folder membership instead.
				continue
			}
			jobs = append(jobs, bodyJob{rowID: m.ID, uid: m.UID})
			if len(jobs) >= maxPendingPerPass {
				return jobs, nil
			}
		}
		if len(messages) < listPage {
			return jobs, nil
		}
	}
}

// fetchBodies downloads and ingests the given messages in bounded batches,
// newest first, so the messages the user most likely wants arrive first. A
// message the server cannot produce (expunged between the header and body
// fetches) simply stays pending and is completed by a later pass.
func (w *Worker) fetchBodies(ctx context.Context, server MailServer, folder store.Folder, jobs []bodyJob) error {
	if len(jobs) == 0 {
		return nil
	}
	byUID := make(map[uint32]bodyJob, len(jobs))
	for _, job := range jobs {
		byUID[job.uid] = job
	}
	sorted := make([]bodyJob, len(jobs))
	copy(sorted, jobs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].uid > sorted[j].uid })

	for start := 0; start < len(sorted); start += bodyBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+bodyBatchSize, len(sorted))
		uids := make([]uint32, 0, end-start)
		for _, job := range sorted[start:end] {
			uids = append(uids, job.uid)
		}
		messages, err := server.FetchBodies(ctx, folder.IMAPPath, uids)
		if err != nil {
			return err
		}
		for _, sm := range messages {
			job, ok := byUID[sm.UID]
			if !ok {
				continue
			}
			if err := w.processMessage(ctx, folder, job.rowID, sm.Body); err != nil {
				return err
			}
		}
	}
	return nil
}

// reconcileMessages applies server authority to flags and existence, unless
// offline actions are still queued: queued changes own their messages' state
// until they have been replayed, so the sync never fights the optimistic
// edits the UI already applied.
func (w *Worker) reconcileMessages(ctx context.Context, server MailServer, folder store.Folder, fresh []ServerMessage, cursor uint32) error {
	queued, err := w.deps.DB.CountActions(ctx)
	if err != nil {
		return err
	}
	if queued > 0 {
		return nil
	}

	serverMessages := fresh
	if cursor > 1 {
		// The new-mail fetch started above the folder's first message; the
		// existence and flag check needs the complete server set. Flags alone
		// are enough, so no headers are transferred.
		serverMessages, err = server.FetchFlags(ctx, folder.IMAPPath, 1)
		if err != nil {
			return err
		}
	}
	serverByUID := make(map[uint32]ServerMessage, len(serverMessages))
	for _, sm := range serverMessages {
		serverByUID[sm.UID] = sm
	}

	messages, err := w.listAllMessages(ctx, folder.ID)
	if err != nil {
		return err
	}
	for _, m := range messages {
		if m.UID == 0 {
			// Moved locally, not yet re-numbered by a target-folder sync.
			continue
		}
		sm, ok := serverByUID[m.UID]
		if !ok {
			// The server no longer knows this message: a deletion made
			// elsewhere wins over the local copy.
			if err := w.deps.DB.DeleteMessage(ctx, m.ID); err != nil {
				return err
			}
			continue
		}
		if want := parseFlags(sm.Flags); m.Flags != want {
			if err := w.deps.DB.SetFlags(ctx, m.ID, want); err != nil {
				return err
			}
		}
	}
	return nil
}

// listAllMessages pages through every stored message of a folder.
func (w *Worker) listAllMessages(ctx context.Context, folderID int64) ([]store.Message, error) {
	var all []store.Message
	for offset := 0; ; offset += listPage {
		messages, err := w.deps.DB.ListMessages(ctx, store.MessageQuery{
			FolderID: folderID,
			Limit:    listPage,
			Offset:   offset,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, messages...)
		if len(messages) < listPage {
			return all, nil
		}
	}
}

// inboxPath is the parked folder for IDLE, or empty when the account has no
// inbox (in which case polling handles everything).
func inboxPath(folders []store.Folder) string {
	for _, f := range folders {
		if f.Type == store.FolderInbox {
			return f.IMAPPath
		}
	}
	return ""
}

type folderFilter func(store.Folder) bool

// foldersWhere filters a folder list.
func foldersWhere(folders []store.Folder, keep folderFilter) []store.Folder {
	var out []store.Folder
	for _, f := range folders {
		if keep(f) {
			out = append(out, f)
		}
	}
	return out
}

func isInbox(f store.Folder) bool { return f.Type == store.FolderInbox }

// orderFoldersForSync sorts folders so the inbox syncs first (new mail
// matters most), then everything else by path for determinism.
func orderFoldersForSync(folders []store.Folder) []store.Folder {
	sorted := make([]store.Folder, len(folders))
	copy(sorted, folders)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := sorted[i].Type == store.FolderInbox, sorted[j].Type == store.FolderInbox
		if ri != rj {
			return ri
		}
		return sorted[i].IMAPPath < sorted[j].IMAPPath
	})
	return sorted
}

// folderType maps a server-reported mailbox role onto a folder type. Any
// unrecognized or absent role becomes a user folder.
func folderType(role string) store.FolderType {
	switch store.FolderType(role) {
	case store.FolderInbox, store.FolderSent, store.FolderDrafts,
		store.FolderArchive, store.FolderTrash, store.FolderJunk:
		return store.FolderType(role)
	default:
		return store.FolderUser
	}
}

// folderDisplayName is the human label of a mailbox: the part after the last
// hierarchy delimiter.
func folderDisplayName(sf ServerFolder) string {
	if sf.Delimiter != "" {
		if i := strings.LastIndex(sf.Path, sf.Delimiter); i >= 0 {
			return sf.Path[i+len(sf.Delimiter):]
		}
	}
	return sf.Path
}
