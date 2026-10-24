package tickets

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/internal/safehttp"
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
	// The welcome message (pings + opening embed) is the ticket's own system
	// post, not conversation: skip it by ID.
	var history []modules.LogEntry
	if !opts.skipHistory {
		history = m.fetchAllMessages(tk.ChannelID, tk.MessageID)
	}
	// 2) Plan the attachment mirror (downloads, outside the lock).
	filesDir := filepath.Join(dataDir, "tickets", tk.GuildID, tk.ID, "files")
	if err := ensurePrivateDir(filesDir); err != nil {
		m.ctx.Logger.Warn("Tickets: mirror mkdir %s: %v", filesDir, err)
	}
	client := safehttp.NewClient(60 * time.Second)
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

	// The archive is durable at this point: the ticket JSON is persisted and
	// the transcript file exists (or its failure is logged above). The channel
	// is now redundant - delete it so closed tickets don't pile up. The
	// channel-deleted path already has no channel to remove.
	if reason != "channel_deleted" && tk.ChannelID != "" {
		if cid, perr := snowflake.Parse(tk.ChannelID); perr == nil {
			if delErr := m.ctx.Rest.DeleteChannel(cid); delErr != nil {
				m.ctx.Logger.Warn("Tickets: failed to delete closed channel %s: %v", tk.ChannelID, delErr)
			}
		}
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
	if err := ensurePrivateDir(filepath.Dir(htmlPath)); err != nil {
		return nil, "", nil, err
	}
	if err := writePrivateFile(htmlPath, []byte(htmlStr)); err != nil {
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

// fetchAllMessages pages the entire channel history (newest→oldest), skipping
// the ticket's welcome message (skipMsgID) when given.
func (m *TicketsModule) fetchAllMessages(channelID, skipMsgID string) []modules.LogEntry {
	var out []modules.LogEntry
	before := snowflake.ID(0)
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return nil
	}
	for range 200 { // hard cap: 200*100 = 20k messages
		batch, err := m.ctx.Rest.GetMessages(cid, 0, before, 0, 100)
		if err != nil || len(batch) == 0 {
			break
		}
		for _, msg := range batch {
			if skipMsgID != "" && msg.ID.String() == skipMsgID {
				before = msg.ID
				continue
			}
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
	sort.SliceStable(log, func(i, j int) bool {
		return log[i].Timestamp.Before(log[j].Timestamp)
	})
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
	f, err := openPrivateFile(dir, name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
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

// archivePlayerHTML renders the standalone transcript's custom media player
// (mirrors the dashboard's .player markup).
func archivePlayerHTML(kind, src string) string {
	tag, cls, extra := "video", "", " playsinline"
	if kind == "audio" {
		tag, cls, extra = "audio", " paudio", ""
	}
	return "<div class='player" + cls + "' data-player><" + tag + " preload='metadata'" + extra + " src='" + html.EscapeString(src) + "'></" + tag + ">" +
		"<div class='pbar'><button class='pbtn pplay' type='button'>▶</button>" +
		"<input class='pseek' type='range' min='0' max='1000' value='0'>" +
		"<span class='ptime'>0:00</span>" +
		"<input class='pvol' type='range' min='0' max='100' value='100' aria-label='Volume'>" +
		"<button class='pbtn pmute' type='button'>🔊</button>" +
		"<button class='pbtn pfull' type='button'>⛶</button>" +
		"<a class='pbtn pdl' href='" + html.EscapeString(src) + "' download>⬇</a></div></div>"
}

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
	b.WriteString("#lb{display:none;position:fixed;inset:0;background:rgba(0,0,0,.85);z-index:100;align-items:center;justify-content:center;cursor:zoom-out}")
	b.WriteString("#lb.open{display:flex}#lb img{max-width:92vw;max-height:92vh;border-radius:6px;cursor:zoom-in;touch-action:none}")
	b.WriteString(".lbtools{position:fixed;top:16px;right:16px;z-index:101;display:flex;gap:8px}")
	b.WriteString(".lbbtn{background:#2f3136;color:#fff;border:1px solid #55585e;border-radius:6px;padding:6px 10px;font:inherit;font-size:13px;cursor:pointer;text-decoration:none}")
	b.WriteString(".player{display:inline-block;width:min(480px,100%);margin-top:6px;background:#101012;border:1px solid #3a3a40;border-radius:8px;overflow:hidden}")
	b.WriteString(".player video,.player audio{display:block;width:100%;background:#000;max-height:360px}")
	b.WriteString(".pbar{display:flex;align-items:center;gap:8px;padding:7px 9px}")
	b.WriteString(".pbtn{border:0;background:none;color:#dcddde;font-size:14px;line-height:1;cursor:pointer;border-radius:4px;padding:2px 4px;text-decoration:none}")
	b.WriteString(".pseek{flex:1;min-width:60px;accent-color:#fff;height:4px}")
	b.WriteString(".pvol{width:64px;accent-color:#fff;height:4px}")
	b.WriteString(".ptime{font-size:12px;color:#a1a1aa;white-space:nowrap}")
	b.WriteString(".paudio .pfull{display:none}.paudio audio{height:40px;max-height:40px}")
	b.WriteString("img.attachment{cursor:zoom-in}")
	b.WriteString("</style></head><body><div class='wrap'>")
	b.WriteString("<h1>" + esc(title) + "</h1>")
	status := "Open"
	if t.Status != "open" {
		status = "Closed"
	}
	b.WriteString(fmt.Sprintf("<div class='meta'>Guild: %s · Ticket: <code>%s</code> · Status: %s · Opened %s</div>",
		esc(guildName), esc(t.ID), status, t.OpenedAt.UTC().Format(time.RFC1123)))

	for _, e := range t.VisibleLog() {
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
				b.WriteString(archivePlayerHTML("video", src))
			case "audio":
				b.WriteString(archivePlayerHTML("audio", src))
			default:
				b.WriteString("<a class='attachment attlink' href='" + esc(src) + "' download>⬇ " + label + "</a>")
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
				b.WriteString("<a class='attachment attlink' href='" + esc(src) + "' download>🎫 " + label + "</a>")
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
				b.WriteString(archivePlayerHTML("video", src))
			case "audio":
				b.WriteString(archivePlayerHTML("audio", src))
			default:
				b.WriteString("<a class='attachment attlink' href='" + esc(src) + "' download>🔗 " + label + "</a>")
			}
		}
		if e.Content == "" && len(e.Attachments) == 0 && len(e.Stickers) == 0 && len(e.Embeds) == 0 && !e.Deleted {
			b.WriteString("<div class='content'>(no content)</div>")
		}
		b.WriteString("</div></div>")
	}
	b.WriteString("</div>")
	b.WriteString("<div id='lb'><div class='lbtools'><a id='lbdl' class='lbbtn' href='#' download>⬇ Download</a><button type='button' id='lbclose' class='lbbtn'>✕</button></div><img id='lbi' alt=''></div>")
	b.WriteString("<script>(function(){var lb=document.getElementById('lb'),im=document.getElementById('lbi'),dl=document.getElementById('lbdl'),s=1,x=0,y=0,d=null;function apply(){im.style.transform='translate('+x+'px,'+y+'px) scale('+s+')';}function close(){lb.classList.remove('open');im.removeAttribute('src');s=1;x=0;y=0;apply();}document.querySelectorAll('img.attachment').forEach(function(i){i.addEventListener('click',function(){im.src=this.src;dl.href=this.src;s=1;x=0;y=0;apply();lb.classList.add('open');});});lb.addEventListener('click',function(e){if(e.target===lb)close();});document.getElementById('lbclose').addEventListener('click',close);im.addEventListener('wheel',function(e){e.preventDefault();s=Math.min(6,Math.max(1,s*(e.deltaY<0?1.15:1/1.15)));if(s===1){x=0;y=0;}apply();},{passive:false});im.addEventListener('mousedown',function(e){if(s<=1)return;e.preventDefault();d={x:e.clientX,y:e.clientY,tx:x,ty:y};});document.addEventListener('mousemove',function(e){if(!d)return;x=d.tx+(e.clientX-d.x);y=d.ty+(e.clientY-d.y);apply();});document.addEventListener('mouseup',function(){d=null;});im.addEventListener('dblclick',function(e){e.preventDefault();if(s>1){s=1;x=0;y=0;}else{s=2.5;}apply();});document.addEventListener('keydown',function(e){if(e.key==='Escape')close();});function fmt(t){if(!isFinite(t)||t<0)t=0;var m=Math.floor(t/60),q=Math.floor(t%60);return m+':'+(q<10?'0':'')+q;}document.querySelectorAll('[data-player]').forEach(function(p){var m=p.querySelector('video,audio'),play=p.querySelector('.pplay'),seek=p.querySelector('.pseek'),time=p.querySelector('.ptime'),mute=p.querySelector('.pmute'),vol=p.querySelector('.pvol'),full=p.querySelector('.pfull');if(!m||!play)return;function sync(){var dur=m.duration||0,cur=m.currentTime||0;seek.value=dur?Math.round(cur/dur*1000):0;time.textContent=fmt(cur)+' / '+fmt(dur);play.textContent=m.paused?'▶':'❚❚';if(vol)vol.value=Math.round((m.muted?0:m.volume)*100);}function toggle(){if(m.paused){var pr=m.play();if(pr&&pr.catch)pr.catch(function(){});}else{m.pause();}}play.addEventListener('click',toggle);m.addEventListener('click',toggle);['play','pause','timeupdate','loadedmetadata'].forEach(function(ev){m.addEventListener(ev,sync);});seek.addEventListener('input',function(){if(m.duration)m.currentTime=seek.value/1000*m.duration;});mute.addEventListener('click',function(){m.muted=!m.muted;mute.textContent=m.muted?'🔇':'🔊';sync();});if(vol)vol.addEventListener('input',function(){m.volume=vol.value/100;m.muted=vol.value==='0';mute.textContent=m.muted?'🔇':'🔊';});if(full)full.addEventListener('click',function(){if(document.fullscreenElement)document.exitFullscreen();else if(p.requestFullscreen)p.requestFullscreen();});sync();});})();</script>")
	b.WriteString("</body></html>")
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
