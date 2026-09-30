package tickets

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// transcript.go — on close: fetch full channel history, merge into the stored
// log, mirror attachments to disk, build ONE self-contained HTML transcript,
// post it to the log channel and record its path. The dashboard serves both.

const maxAttachmentBytes = 25 << 20 // 25MB per file

// closeOptions controls the finalizeTicket tail.
type closeOptions struct {
	skipLock    bool
	skipHistory bool
}

// markClosed marks a ticket closed (idempotent). Returns false if it was
// already closed. reason is the CloseReason value ("user" | "channel_deleted").
// Callers hold the serialization point: it either runs inside a store
// mutation or is given a copy nobody else can reach, so no module lock is
// taken here (the store lock is what makes the close atomic).
func (m *TicketsModule) markClosed(tk *modules.Ticket, byUserID, byName, reason string) bool {
	if tk.Status != "open" {
		return false
	}
	now := time.Now().UTC()
	tk.Status = "closed"
	tk.ClosedAt = now
	tk.CloseReason = reason
	tk.Log = append(tk.Log, modules.LogEntry{
		MsgID: "system-close-" + tk.ID, AuthorID: byUserID,
		AuthorName: byName, IsBot: true,
		Timestamp: now, Content: "_Ticket closed._",
	})
	return true
}

// finalizeTicket runs the close tail: lock channel → history merge →
// attachment backfill → HTML transcript → (optionally) log-channel post.
// Runs in a recovered goroutine (see CloseTicket / finalizeDeletedTicket).
//
// The slow parts (paging history, downloads, the guild-name lookup) happen
// outside the store lock and are merged onto the CURRENT stored copy by a
// store mutation, so an entry appended by the live event hooks while the
// tail ran is never overwritten by a stale snapshot.
func (m *TicketsModule) finalizeTicket(tk *modules.Ticket, g TypeConfig, closedBy, reason string, opts closeOptions, postToLog bool) {
	if !opts.skipLock {
		m.lockTicketChannel(tk, g)
	}
	dataDir := m.ctx.DataDir

	// 1) Fetch full history (REST, outside the lock). Edits/deletes were
	// already tracked live; this backfills anything the event hooks missed.
	var history []modules.LogEntry
	if !opts.skipHistory {
		history = m.fetchAllMessages(tk.ChannelID)
	}
	// 2) Plan the attachment mirror (downloads, outside the lock).
	filesDir := filepath.Join(dataDir, "tickets", tk.GuildID, tk.ID, "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		m.ctx.Logger.Warn("Tickets: mirror mkdir %s: %v", filesDir, err)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	fixes := planTicketMedia(client, tk, filesDir, dataDir, m.ctx.Logger)

	// 3) Merge history + media fixes + the close fields onto the stored copy.
	// The close fields are re-asserted because the caller's copy may never
	// have been persisted (finalizeDeletedTicket's channel-delete path).
	post, _, err := m.store.mutate(tk.GuildID, tk.ID, func(cur *modules.Ticket) bool {
		dirty := false
		if cur.Status == "open" {
			m.markClosed(cur, closedBy, closedBy, reason)
			dirty = true
		}
		if cur.ClosedAt.IsZero() {
			cur.ClosedAt = time.Now().UTC()
			dirty = true
		}
		if cur.CloseReason == "" {
			cur.CloseReason = reason
			dirty = true
		}
		if mergeEntriesInto(cur, history) {
			dirty = true
		}
		if applyMediaFixes(cur, fixes) {
			dirty = true
		}
		return dirty
	})
	if err != nil || post == nil {
		post = tk
	}

	// 4) Render + persist the HTML transcript. The file is a CACHE of the
	// stored log, not a source of truth: renderTranscriptToDisk reads the
	// store fresh, the reconcile loop below repairs anything that landed
	// between that read and the file write, and the file stays regenerable
	// on demand via RefreshTranscript.
	_, absPath, rendered, err := m.renderTranscriptToDisk(post.GuildID, post.ID)
	if err != nil {
		m.ctx.Logger.Error("Tickets: transcript render failed for %s: %v", post.ID, err)
	} else {
		// Bounded reconcile: a mutation that lands between the render's store
		// read and its file write leaves the file behind the store. Re-render
		// up to 3 more passes until the file matches the stored log again.
		divergent := false
		for range 3 {
			fresh, ferr := m.store.load(post.GuildID, post.ID)
			if ferr != nil || fresh == nil {
				break
			}
			if reflect.DeepEqual(fresh.Log, rendered.Log) {
				divergent = false
				break
			}
			divergent = true
			var rerr error
			_, absPath, rendered, rerr = m.renderTranscriptToDisk(post.GuildID, post.ID)
			if rerr != nil {
				m.ctx.Logger.Warn("Tickets: transcript re-render failed for %s: %v", post.ID, rerr)
				break
			}
		}
		if divergent {
			// Still divergent after the bound: proceed with the last render —
			// the cache is refreshable on demand via RefreshTranscript.
			m.ctx.Logger.Warn("Tickets: transcript for %s is stale after the reconcile bound; regenerate via RefreshTranscript", post.ID)
		}
		if rel, rerr := filepath.Rel(dataDir, absPath); rerr == nil {
			post.TranscriptPath = rel
		}
	}

	// Hand the persisted state back to the caller's copy.
	*tk = *post

	if postToLog && err == nil {
		m.postTranscriptToLogChannel(tk, g, absPath, closedBy)
	}
}

// renderTranscriptToDisk re-renders the ticket's HTML transcript from its
// CURRENT stored log, writes it atomically, and records TranscriptPath via a
// store mutation when it changed. It returns the rendered bytes, the absolute
// path, and the exact snapshot that was rendered (the caller uses the snapshot
// to detect that something landed while it was writing).
func (m *TicketsModule) renderTranscriptToDisk(guildID, ticketID string) (data []byte, absPath string, rendered *modules.Ticket, err error) {
	tk, err := m.store.load(guildID, ticketID)
	if err != nil {
		return nil, "", nil, err
	}
	if tk == nil {
		return nil, "", nil, fmt.Errorf("ticket %s/%s not found", guildID, ticketID)
	}
	htmlStr := buildTranscriptHTML(tk, m.resolveGuildName(guildID))
	htmlPath := filepath.Join(m.ctx.DataDir, "tickets", guildID, ticketID+".html")
	if err := os.MkdirAll(filepath.Dir(htmlPath), 0755); err != nil {
		return nil, "", nil, err
	}
	// tmp + rename (the house pattern, cf. writeIndexLocked); 0644 is what
	// this file has always been.
	tmp := htmlPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(htmlStr), 0644); err != nil {
		return nil, "", nil, err
	}
	if err := os.Rename(tmp, htmlPath); err != nil {
		return nil, "", nil, err
	}
	// Persist the path RELATIVE to DataDir (the dashboard serves it that way);
	// a failed persist is a WARN — the file itself is what matters.
	if rel, rerr := filepath.Rel(m.ctx.DataDir, htmlPath); rerr == nil {
		if _, _, perr := m.store.mutate(guildID, ticketID, func(cur *modules.Ticket) bool {
			if cur.TranscriptPath == rel {
				return false
			}
			cur.TranscriptPath = rel
			return true
		}); perr != nil {
			m.ctx.Logger.Warn("Tickets: transcript path persist failed for %s: %v", ticketID, perr)
		}
	}
	return []byte(htmlStr), htmlPath, tk, nil
}

// RefreshTranscript implements modules.TicketTranscript: it rebuilds the
// ticket's HTML transcript from its CURRENT stored log, rewrites the on-disk
// file, and returns the rendered bytes.
func (m *TicketsModule) RefreshTranscript(guildID, ticketID string) ([]byte, error) {
	if !m.isLoaded() {
		return nil, fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return nil, fmt.Errorf("invalid guildID")
	}
	if !validTicketID(ticketID) {
		return nil, fmt.Errorf("invalid ticket ID")
	}
	data, _, _, err := m.renderTranscriptToDisk(guildID, ticketID)
	return data, err
}

// lockTicketChannel strips send perms from opener/helpers/members; history
// stays readable forever.
func (m *TicketsModule) lockTicketChannel(tk *modules.Ticket, g TypeConfig) {
	cid, err := snowflake.Parse(tk.ChannelID)
	if err != nil {
		return
	}
	ows := overwritesFor(tk.GuildID, tk.OpenerID, g.HelperRoles, tk.Members, true, m.botSelfID)
	update := discord.GuildTextChannelUpdate{
		PermissionOverwrites: &ows,
	}
	if _, err := m.ctx.Rest.UpdateChannel(cid, update); err != nil {
		m.ctx.Logger.Warn("Tickets: lock overwrites failed on %s: %v", tk.ID, err)
	}
}

// fetchAllMessages pages the entire channel history (newest→oldest).
func (m *TicketsModule) fetchAllMessages(channelID string) []modules.LogEntry {
	var out []modules.LogEntry
	before := snowflake.ID(0)
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return nil
	}
	for i := 0; i < 200; i++ { // hard cap: 200*100 = 20k messages
		batch, err := m.ctx.Rest.GetMessages(cid, 100, before, 0, 0)
		if err != nil || len(batch) == 0 {
			break
		}
		for _, msg := range batch {
			out = append(out, messageToEntry(msg))
			before = msg.ID
		}
		if len(batch) < 100 {
			break
		}
	}
	// reverse → chronological
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// messageToEntry converts a discord.Message into a transcript LogEntry.
func messageToEntry(msg discord.Message) modules.LogEntry {
	entry := modules.LogEntry{
		MsgID:       msg.ID.String(),
		Timestamp:   msg.CreatedAt,
		Content:     msg.Content,
		Attachments: classifyAttachments(msg.Attachments),
		Stickers:    classifyStickers(msg.StickerItems),
		Embeds:      classifyEmbeds(msg.Embeds),
	}
	if msg.Member != nil && msg.Member.User.ID != 0 {
		entry.AuthorID = msg.Member.User.ID.String()
		entry.AuthorName = msg.Member.EffectiveName()
		entry.IsBot = msg.Member.User.Bot
	} else if msg.Author.ID != 0 {
		entry.AuthorID = msg.Author.ID.String()
		entry.AuthorName = msg.Author.EffectiveName()
		entry.IsBot = msg.Author.Bot
	}
	return entry
}

// mergeEntriesInto appends every history entry whose MsgID is not already in
// tk.Log and re-sorts chronologically when anything was added. Pure and
// lock-free: it runs on the store's private copy inside a mutation, and on a
// bare copy nobody else can reach. Returns whether anything changed.
func mergeEntriesInto(tk *modules.Ticket, history []modules.LogEntry) bool {
	if len(history) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, e := range tk.Log {
		have[e.MsgID] = true
	}
	added := false
	for _, e := range history {
		if !have[e.MsgID] {
			tk.Log = append(tk.Log, e)
			have[e.MsgID] = true
			added = true
		}
	}
	if added {
		sortLogByTime(tk.Log)
	}
	return added
}

func sortLogByTime(log []modules.LogEntry) {
	for i := 1; i < len(log); i++ {
		for j := i; j > 0 && log[j].Timestamp.Before(log[j-1].Timestamp); j-- {
			log[j], log[j-1] = log[j-1], log[j]
		}
	}
}

func downloadAttachment(client *http.Client, url, dir, prefix, filename string, maxBytes int64) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	name := filename
	if name == "" {
		name = fmt.Sprintf("file-%d", time.Now().UnixNano())
	}
	name = filepath.Base(filepath.FromSlash(strings.ReplaceAll(name, "\\", "_")))
	if name == "." || name == ".." || name == "/" {
		name = "file"
	}
	name = prefix + "-" + name
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if n > maxBytes {
		_ = os.Remove(path)
		return "", fmt.Errorf("attachment exceeds %d bytes", maxBytes)
	}
	return path, closeErr
}

// ── HTML builder (pure, unit-tested) ─────────────────────────────────────

// buildTranscriptHTML renders a chat-exporter-style standalone page from the
// stored log. Attachments reference LocalPath when mirrored (relative),
// falling back to CDN URLs.
func buildTranscriptHTML(t *modules.Ticket, guildName string) string {
	var b strings.Builder
	title := "ticket-" + t.ID
	esc := html.EscapeString
	b.WriteString("<!doctype html><html><head><meta charset='utf-8'>")
	b.WriteString("<title>" + esc(title) + "</title><style>")
	b.WriteString("body{background:#36393f;color:#dcddde;font-family:'gg sans',Segoe UI,Arial,sans-serif;margin:0;padding:24px}")
	b.WriteString(".wrap{max-width:900px;margin:0 auto}")
	b.WriteString("h1{font-size:18px;color:#fff;border-bottom:1px solid #42454a;padding-bottom:8px}")
	b.WriteString(".meta{color:#8e9297;font-size:13px;margin-bottom:16px}")
	b.WriteString(".msg{display:flex;gap:12px;padding:8px 4px;border-bottom:1px solid #40444b}")
	b.WriteString(".avatar{width:40px;height:40px;border-radius:50%;flex:none;background:#5865F2;display:flex;align-items:center;justify-content:center;font-weight:bold;color:#fff}")
	b.WriteString(".body{min-width:0}.author{font-weight:bold;color:#fff}")
	b.WriteString(".time{color:#72767d;font-size:12px;margin-left:6px}")
	b.WriteString(".content{margin-top:2px;white-space:pre-wrap;word-wrap:break-word}")
	b.WriteString(".deleted .content{text-decoration:line-through;opacity:.5}")
	b.WriteString(".edited{color:#72767d;font-size:11px}")
	b.WriteString(".att{display:block;margin-top:6px;max-width:480px;border-radius:6px}")
	b.WriteString("img.attachment{max-width:min(420px,100%)}")
	b.WriteString("video.attachment{max-width:min(480px,100%)}audio.attachment{width:100%}")
	b.WriteString(".attlink{color:#00aff4}")
	b.WriteString("</style></head><body><div class='wrap'>")
	b.WriteString("<h1>" + esc(title) + "</h1>")
	status := "Open"
	if t.Status != "open" {
		status = "Closed"
	}
	b.WriteString(fmt.Sprintf("<div class='meta'>Guild: %s · Ticket: <code>%s</code> · Status: %s · Opened %s</div>",
		esc(guildName), esc(t.ID), status, t.OpenedAt.UTC().Format(time.RFC1123)))

	for _, e := range t.Log {
		cls := "msg"
		if e.Deleted {
			cls += " deleted"
		}
		initial := "?"
		if r := []rune(e.AuthorName); len(r) > 0 {
			initial = strings.ToUpper(string(r[0]))
		}
		b.WriteString("<div class='" + cls + "' data-msg='" + esc(e.MsgID) + "'>")
		b.WriteString("<div class='avatar'>" + esc(initial) + "</div><div class='body'>")
		name := e.AuthorName
		if name == "" {
			name = e.AuthorID
		}
		b.WriteString("<span class='author'>" + esc(name) + "</span>" +
			"<span class='time'>" + e.Timestamp.UTC().Format("Jan 2, 2006 15:04") + "</span>")
		if e.Edited {
			b.WriteString(" <span class='edited'>(edited)</span>")
		}
		if e.Content != "" {
			b.WriteString("<div class='content'>" + esc(e.Content) + "</div>")
		}
		// Media URLs already rendered in this entry (attachments + stickers),
		// so a link embed duplicating one is skipped (D5b).
		renderedURLs := make(map[string]bool)
		for _, a := range e.Attachments {
			src := a.LocalPath
			if src == "" {
				src = a.URL
			}
			if a.URL != "" {
				renderedURLs[a.URL] = true
			}
			label := esc(a.Filename)
			switch a.Kind {
			case "image":
				b.WriteString("<img class='attachment img' loading='lazy' alt='" + label + "' src='" + esc(src) + "'>")
			case "video":
				b.WriteString("<video class='attachment' controls preload='metadata' src='" + esc(src) + "'></video>")
			case "audio":
				b.WriteString("<audio class='attachment' controls preload='metadata' src='" + esc(src) + "'></audio>")
			default:
				b.WriteString("<a class='attachment attlink' href='" + esc(src) + "'>📎 " + label + "</a>")
			}
		}
		for _, s := range e.Stickers {
			src := s.LocalPath
			if src == "" {
				src = s.URL
			}
			if s.URL != "" {
				renderedURLs[s.URL] = true
			}
			label := esc(s.Filename)
			// Lottie stickers (.json) can't render as <img>; link them.
			// png/gif render inline (D5a).
			ext := strings.ToLower(filepath.Ext(strings.Split(src, "?")[0]))
			if ext == ".png" || ext == ".gif" {
				b.WriteString("<img class='attachment' alt='" + label + "' src='" + esc(src) + "'>")
			} else {
				b.WriteString("<a class='attachment attlink' href='" + esc(src) + "'>🎫 " + label + "</a>")
			}
		}
		for _, em := range e.Embeds {
			src := em.LocalPath
			if src == "" {
				src = em.URL
			}
			if src == "" {
				continue
			}
			// Skip a link embed duplicating a media URL already rendered above.
			if em.Kind == "link" && em.URL != "" && renderedURLs[em.URL] {
				continue
			}
			label := esc(em.Filename)
			switch em.Kind {
			case "image":
				b.WriteString("<img class='attachment img' loading='lazy' alt='" + label + "' src='" + esc(src) + "'>")
			case "video":
				b.WriteString("<video class='attachment' controls preload='metadata' src='" + esc(src) + "'></video>")
			case "audio":
				b.WriteString("<audio class='attachment' controls preload='metadata' src='" + esc(src) + "'></audio>")
			default:
				b.WriteString("<a class='attachment attlink' href='" + esc(src) + "'>🔗 " + label + "</a>")
			}
		}
		if e.Content == "" && len(e.Attachments) == 0 && len(e.Stickers) == 0 && len(e.Embeds) == 0 && !e.Deleted {
			b.WriteString("<div class='content'>(no content)</div>")
		}
		b.WriteString("</div></div>")
	}
	b.WriteString("</div></body></html>")
	return b.String()
}

// postTranscriptToLogChannel uploads the HTML file to the configured log
// channel with a "Ticket closed" summary embed.
func (m *TicketsModule) postTranscriptToLogChannel(tk *modules.Ticket, g TypeConfig, htmlPath, closedBy string) {
	cfg := m.guildConfig(tk.GuildID)
	m.mu.RLock()
	logCh := cfg.LogChannel
	m.mu.RUnlock()
	if logCh == "" {
		return
	}
	chID, err := snowflake.Parse(logCh)
	if err != nil {
		return
	}
	f, err := os.Open(htmlPath)
	if err != nil {
		return
	}
	defer f.Close()
	label := g.Label
	if label == "" {
		label = tk.EffectiveType()
	}
	title, desc := closeLogEmbed(tk, g, closedBy)
	create := discord.MessageCreate{
		Content: fmt.Sprintf("📜 Transcript of **%s** (`%s`)", label, tk.ID),
		Embeds:  []discord.Embed{embedInfo(title, desc)},
		Files:   []*discord.File{{Name: "ticket-" + tk.ID + ".html", Reader: f}},
	}
	if _, err := m.ctx.Rest.CreateMessage(chID, create); err != nil {
		m.ctx.Logger.Warn("Tickets: transcript upload failed: %v", err)
	}
}
