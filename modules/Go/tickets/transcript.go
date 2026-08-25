package main

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// closeWithTranscript is the v2 close tail: lock channel → mirror files →
// build HTML → post to log channel. Runs in a recovered goroutine (see
// CloseTicket); closedBy is the actual closer's user ID for the header line.
func (m *TicketsModule) closeWithTranscript(tk *modules.Ticket, g TypeConfig, closedBy string) {
	m.lockTicketChannel(tk, g)

	m.mu.RLock()
	dataDir := m.ctx.DataDir
	m.mu.RUnlock()

	// 1) Fetch full history and merge into the stored log (edits/deletes
	// already tracked live; this backfills anything the event hooks missed).
	if msgs := m.fetchAllMessages(tk.ChannelID); len(msgs) > 0 {
		m.mergeHistoryIntoLog(tk, msgs)
	}
	// 2) Mirror attachments next to the ticket file.
	filesDir := filepath.Join(dataDir, "tickets", tk.GuildID, tk.ID, "files")
	m.mirrorAttachments(tk, filesDir)
	_ = m.store.save(tk)

	// 3) Build + post + persist transcript.
	htmlStr := buildTranscriptHTML(tk, m.resolveGuildName(tk.GuildID))
	htmlPath := filepath.Join(dataDir, "tickets", tk.GuildID, tk.ID+".html")
	if err := os.WriteFile(htmlPath, []byte(htmlStr), 0644); err != nil {
		m.ctx.Logger.Error("Tickets: transcript write failed for %s: %v", tk.ID, err)
		return
	}
	rel, err := filepath.Rel(dataDir, htmlPath)
	if err == nil {
		tk.TranscriptPath = rel
		_ = m.store.save(tk)
	}
	m.postTranscriptToLogChannel(tk, g, htmlPath, closedBy)
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

// mergeHistoryIntoLog adds entries not already present (by MsgID).
func (m *TicketsModule) mergeHistoryIntoLog(tk *modules.Ticket, history []modules.LogEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	have := map[string]bool{}
	for _, e := range tk.Log {
		have[e.MsgID] = true
	}
	added := false
	for _, e := range history {
		if !have[e.MsgID] {
			tk.Log = append(tk.Log, e)
			added = true
		}
	}
	if added {
		sortLogByTime(tk.Log)
	}
}

func sortLogByTime(log []modules.LogEntry) {
	for i := 1; i < len(log); i++ {
		for j := i; j > 0 && log[j].Timestamp.Before(log[j-1].Timestamp); j-- {
			log[j], log[j-1] = log[j-1], log[j]
		}
	}
}

// mirrorAttachments downloads every referenced attachment into filesDir and
// rewrites Media.LocalPath relative to dataDir. Files are prefixed with
// msgID-index so same-named uploads never overwrite each other.
func (m *TicketsModule) mirrorAttachments(tk *modules.Ticket, filesDir string) {
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		return
	}
	client := &http.Client{Timeout: 60 * time.Second}
	m.mu.RLock()
	dataDir := m.ctx.DataDir
	m.mu.RUnlock()

	for i := range tk.Log {
		entry := &tk.Log[i]
		for j := range entry.Attachments {
			med := &entry.Attachments[j]
			if med.URL == "" || med.LocalPath != "" || med.Kind == "sticker" {
				continue
			}
			prefix := fmt.Sprintf("%s-%d", entry.MsgID, j)
			local, err := downloadAttachment(client, med.URL, filesDir, prefix, med.Filename, maxAttachmentBytes)
			if err != nil {
				m.ctx.Logger.Warn("Tickets: attachment %s failed: %v", med.Filename, err)
				continue
			}
			if rel, err := filepath.Rel(dataDir, local); err == nil {
				med.LocalPath = rel
			}
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
		for _, a := range e.Attachments {
			src := a.LocalPath
			if src == "" {
				src = a.URL
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
			b.WriteString("<img class='attachment' alt='" + esc(s.Filename) + "' src='" + esc(src) + "'>")
		}
		if e.Content == "" && len(e.Attachments) == 0 && len(e.Stickers) == 0 && !e.Deleted {
			b.WriteString("<div class='content'>(no content)</div>")
		}
		b.WriteString("</div></div>")
	}
	b.WriteString("</div></body></html>")
	return b.String()
}

// postTranscriptToLogChannel uploads the HTML file to the configured log
// channel with a short summary embed.
func (m *TicketsModule) postTranscriptToLogChannel(tk *modules.Ticket, g TypeConfig, htmlPath, closedBy string) {
	m.mu.RLock()
	logCh := ""
	if m.cfg != nil {
		logCh = m.cfg.LogChannel
	}
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
	claimLine := ""
	if tk.ClaimerID != "" {
		claimLine = fmt.Sprintf(" · claimed by <@%s>", tk.ClaimerID)
	}
	create := discord.MessageCreate{
		Content: fmt.Sprintf("📜 Transcript of **%s** (`%s`) — opened by <@%s>%s · closed by <@%s>",
			label, tk.ID, tk.OpenerID, claimLine, closedBy),
		Files: []*discord.File{{Name: "ticket-" + tk.ID + ".html", Reader: f}},
	}
	if _, err := m.ctx.Rest.CreateMessage(chID, create); err != nil {
		m.ctx.Logger.Warn("Tickets: transcript upload failed: %v", err)
	}
}
