package tickets

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/misfit/bot/modules"
)

// mirrorJob is one log entry whose non-link media should be downloaded.
type mirrorJob struct {
	guildID  string
	ticketID string
	entry    modules.LogEntry
}

// mirrorQueue is a bounded queue drained by a SINGLE worker. One worker (not a
// pool) because same-ticket jobs would race the store load/save.
type mirrorQueue struct {
	ch     chan mirrorJob
	logger modules.Logger
	stop   chan struct{}
}

func newMirrorQueue(logger modules.Logger) *mirrorQueue {
	return &mirrorQueue{ch: make(chan mirrorJob, 256), logger: logger}
}

// setStop wires the module stop channel so enqueue can bail after OnUnload.
func (q *mirrorQueue) setStop(stop chan struct{}) { q.stop = stop }

// enqueue adds a job without blocking. It checks stop first (so a post-unload
// event can never panic on a closed channel) and drops the job with a WARN on
// overflow — the close-tail backfill re-mirrors everything at close time.
func (q *mirrorQueue) enqueue(job mirrorJob) {
	if q.stop != nil {
		select {
		case <-q.stop:
			return
		default:
		}
	}
	select {
	case q.ch <- job:
	default:
		q.logger.Warn("Tickets: mirror queue full; dropping %s/%s", job.guildID, job.ticketID)
	}
}

// run is the single worker: for each job it loads the ticket, downloads every
// non-link media item lacking a LocalPath into the ticket's files dir, and
// records the results in a single store mutation. The downloads happen
// outside the store lock (plan), and the fixes are merged onto the CURRENT
// stored copy (apply), so a concurrent log append is never dropped. Per-file
// errors WARN and never abort the job. Exits on stop.
func (q *mirrorQueue) run(dataDir string, st *store, stop <-chan struct{}) {
	client := &http.Client{Timeout: 60 * time.Second}
	for {
		select {
		case <-stop:
			return
		case job := <-q.ch:
			tk, err := st.load(job.guildID, job.ticketID)
			if err != nil || tk == nil {
				continue
			}
			filesDir := filepath.Join(ticketsRoot(dataDir), job.guildID, job.ticketID, "files")
			if err := os.MkdirAll(filesDir, 0755); err != nil {
				q.logger.Warn("Tickets: mirror mkdir %s: %v", filesDir, err)
				continue
			}
			fixes := planTicketMedia(client, tk, filesDir, dataDir, q.logger)
			if len(fixes) == 0 {
				continue
			}
			_, _, _ = st.mutate(job.guildID, job.ticketID, func(tk *modules.Ticket) bool {
				return applyMediaFixes(tk, fixes)
			})
		}
	}
}

// mediaFix is one downloaded file that still has to be recorded on a log
// entry. Planning happens outside the store lock (it does network I/O);
// applying merges the fix onto the CURRENT stored copy by MsgID + index, so a
// concurrent append or edit is never overwritten.
type mediaFix struct {
	msgID     string
	kind      string // "attachments" | "stickers" | "embeds"
	index     int
	localPath string
}

// planEntryMedia downloads every non-link media item in the entry that has a
// URL and no LocalPath yet, returning the fixes to apply. No store lock is
// held and no ticket state is touched: the downloads are pure I/O.
func planEntryMedia(client *http.Client, entry *modules.LogEntry, filesDir, dataDir string, log modules.Logger) []mediaFix {
	var fixes []mediaFix
	download := func(kind string, med *modules.Media, j int, prefix string) {
		if med.URL == "" || med.LocalPath != "" || med.Kind == "link" {
			return
		}
		local, err := downloadAttachment(client, med.URL, filesDir, prefix, med.Filename, maxAttachmentBytes)
		if err != nil {
			log.Warn("Tickets: mirror %s failed: %v", med.Filename, err)
			return
		}
		rel, err := filepath.Rel(dataDir, local)
		if err != nil {
			return
		}
		fixes = append(fixes, mediaFix{msgID: entry.MsgID, kind: kind, index: j, localPath: rel})
	}
	for j := range entry.Attachments {
		download("attachments", &entry.Attachments[j], j, fmt.Sprintf("%s-att%d", entry.MsgID, j))
	}
	for j := range entry.Stickers {
		download("stickers", &entry.Stickers[j], j, fmt.Sprintf("%s-stk%d", entry.MsgID, j))
	}
	for j := range entry.Embeds {
		download("embeds", &entry.Embeds[j], j, fmt.Sprintf("%s-emb%d", entry.MsgID, j))
	}
	return fixes
}

// planTicketMedia plans the media downloads for every entry in a ticket
// SNAPSHOT. The snapshot is only read; the returned fixes are applied to the
// current stored copy with applyMediaFixes.
func planTicketMedia(client *http.Client, tk *modules.Ticket, filesDir, dataDir string, log modules.Logger) []mediaFix {
	var fixes []mediaFix
	for i := range tk.Log {
		fixes = append(fixes, planEntryMedia(client, &tk.Log[i], filesDir, dataDir, log)...)
	}
	return fixes
}

// applyFixesToLog records planned downloads on a log slice, matching entries
// by MsgID and media by kind + index. Only still-empty LocalPath fields are
// filled, so a concurrent append or a second mirror pass is never clobbered.
// Returns true if anything was set.
func applyFixesToLog(log []modules.LogEntry, fixes []mediaFix) bool {
	changed := false
	byID := make(map[string]*modules.LogEntry, len(log))
	for i := range log {
		byID[log[i].MsgID] = &log[i]
	}
	for _, f := range fixes {
		entry := byID[f.msgID]
		if entry == nil {
			continue
		}
		var med *modules.Media
		switch f.kind {
		case "attachments":
			if f.index < len(entry.Attachments) {
				med = &entry.Attachments[f.index]
			}
		case "stickers":
			if f.index < len(entry.Stickers) {
				med = &entry.Stickers[f.index]
			}
		case "embeds":
			if f.index < len(entry.Embeds) {
				med = &entry.Embeds[f.index]
			}
		}
		if med == nil || med.LocalPath != "" {
			continue
		}
		med.LocalPath = f.localPath
		changed = true
	}
	return changed
}

// applyMediaFixes merges planned downloads onto the current copy of a ticket.
func applyMediaFixes(tk *modules.Ticket, fixes []mediaFix) bool {
	return applyFixesToLog(tk.Log, fixes)
}

// entryHasMirrorableMedia reports whether the entry has any non-link media
// with a URL and no LocalPath yet (i.e. something the mirror worker should
// download).
func entryHasMirrorableMedia(e modules.LogEntry) bool {
	for _, m := range e.Attachments {
		if m.URL != "" && m.LocalPath == "" && m.Kind != "link" {
			return true
		}
	}
	for _, m := range e.Stickers {
		if m.URL != "" && m.LocalPath == "" && m.Kind != "link" {
			return true
		}
	}
	for _, m := range e.Embeds {
		if m.URL != "" && m.LocalPath == "" && m.Kind != "link" {
			return true
		}
	}
	return false
}
