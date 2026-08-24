package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/misfit/bot/modules"
)

// store persists tickets as one JSON file each under
// <dataDir>/tickets/<guildID>/<ticketID>.json, with a per-guild index.json of
// open ticket IDs for fast dashboard lists. All state is also held in memory;
// every mutation flushes to disk atomically (tmp + rename).
type store struct {
	mu      sync.RWMutex
	dataDir string
	// tickets[guildID][ticketID] — open tickets only; closed ones are read
	// from disk on demand so memory stays small.
	tickets map[string]map[string]*modules.Ticket
	// reserved tracks sequence numbers allocated but not yet persisted
	// ("guild|group|seq"), so concurrent opens cannot collide (see reserveSeq).
	reserved map[string]bool
}

func ticketsRoot(dataDir string) string {
	return filepath.Join(dataDir, "tickets")
}

// openStore loads all persisted tickets into memory. Closed tickets are
// loaded too (they are few) and then dropped from the live map — GetTicket
// reads them from disk.
func openStore(dataDir string) (*store, error) {
	s := &store{dataDir: dataDir, tickets: map[string]map[string]*modules.Ticket{}, reserved: map[string]bool{}}
	root := ticketsRoot(dataDir)
	guilds, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	for _, g := range guilds {
		if !g.IsDir() {
			continue
		}
		guildID := g.Name()
		files, err := os.ReadDir(filepath.Join(root, guildID))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || f.Name() == "index.json" || filepath.Ext(f.Name()) != ".json" {
				continue
			}
			tk, err := readTicketFile(filepath.Join(root, guildID, f.Name()))
			if err != nil || tk == nil {
				continue // corrupt file: skip, never block startup
			}
			if s.tickets[guildID] == nil {
				s.tickets[guildID] = map[string]*modules.Ticket{}
			}
			if tk.Status == "open" {
				s.tickets[guildID][tk.ID] = tk
			}
		}
	}
	return s, nil
}

// readTicketFile decodes one ticket JSON; returns (nil, nil) on absence.
func readTicketFile(path string) (*modules.Ticket, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tk modules.Ticket
	if err := json.Unmarshal(raw, &tk); err != nil {
		return nil, fmt.Errorf("corrupt ticket file %s: %w", path, err)
	}
	return &tk, nil
}

// copyTicket returns a deep copy so the store's map never shares mutable
// state with callers (single-mutex ownership: mutations happen on private
// copies, save() re-applies them under s.mu).
func copyTicket(tk *modules.Ticket) *modules.Ticket {
	if tk == nil {
		return nil
	}
	out := *tk
	if tk.Log != nil {
		out.Log = make([]modules.LogEntry, len(tk.Log))
		for i, e := range tk.Log {
			out.Log[i] = e
			if e.Attachments != nil {
				out.Log[i].Attachments = append([]modules.Media(nil), e.Attachments...)
			}
			if e.Stickers != nil {
				out.Log[i].Stickers = append([]modules.Media(nil), e.Stickers...)
			}
		}
	}
	return &out
}

// save persists one ticket (memory + disk, atomic write). The passed ticket
// is copied into the store; the caller keeps ownership of its own object.
// A successful save releases any sequence reservation for this ticket.
func (s *store) save(tk *modules.Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := copyTicket(tk)
	if grp, seq, ok := parseTicketID(cp.ID); ok {
		delete(s.reserved, seqKey(cp.GuildID, grp, seq))
	}
	dir := filepath.Join(ticketsRoot(s.dataDir), cp.GuildID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, cp.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if s.tickets[cp.GuildID] == nil {
		s.tickets[cp.GuildID] = map[string]*modules.Ticket{}
	}
	if cp.Status == "open" {
		s.tickets[cp.GuildID][cp.ID] = cp
	} else {
		delete(s.tickets[cp.GuildID], cp.ID)
	}
	return s.writeIndexLocked(cp.GuildID)
}

// load returns a PRIVATE COPY of the ticket — callers may mutate freely and
// persist with save(). (nil, nil) when not found.
func (s *store) load(guildID, ticketID string) (*modules.Ticket, error) {
	s.mu.RLock()
	if m, ok := s.tickets[guildID]; ok {
		if tk, ok := m[ticketID]; ok {
			cp := copyTicket(tk)
			s.mu.RUnlock()
			return cp, nil
		}
	}
	s.mu.RUnlock()
	tk, err := readTicketFile(filepath.Join(ticketsRoot(s.dataDir), guildID, ticketID+".json"))
	if err != nil || tk == nil {
		return tk, err
	}
	return copyTicket(tk), nil
}

// listOpen returns summaries of every open ticket in the guild, oldest first.
func (s *store) listOpen(guildID string) []modules.TicketSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []modules.TicketSummary
	for _, tk := range s.tickets[guildID] {
		out = append(out, summaryOf(tk))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OpenedAt.Before(out[j].OpenedAt) })
	return out
}

// openIDs lists open ticket IDs from the on-disk index (rebuilt when absent).
func (s *store) openIDs(guildID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openIDsLocked(guildID)
}

func (s *store) openIDsLocked(guildID string) []string {
	idxPath := filepath.Join(ticketsRoot(s.dataDir), guildID, "index.json")
	raw, err := os.ReadFile(idxPath)
	if err == nil {
		var ids []string
		if json.Unmarshal(raw, &ids) == nil {
			return ids
		}
	}
	// Rebuild from directory scan (single optional migration).
	entries, err := os.ReadDir(filepath.Join(ticketsRoot(s.dataDir), guildID))
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "index.json" || !strings.HasSuffix(name, ".json") {
			continue
		}
		tk, err := readTicketFile(filepath.Join(ticketsRoot(s.dataDir), guildID, name))
		if err == nil && tk != nil && tk.Status == "open" {
			ids = append(ids, tk.ID)
		}
	}
	sort.Strings(ids)
	out, _ := json.Marshal(ids)
	_ = os.WriteFile(idxPath, out, 0644)
	return ids
}

// writeIndexLocked rewrites index.json from the in-memory open set.
func (s *store) writeIndexLocked(guildID string) error {
	ids := make([]string, 0, len(s.tickets[guildID]))
	for id := range s.tickets[guildID] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	path := filepath.Join(ticketsRoot(s.dataDir), guildID, "index.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// nextSeq returns the next per-guild+group sequence number under the WRITE
// lock. nextSeq and save both serialize on s.mu, so a concurrent open for
// the same group cannot allocate a duplicate number: the second nextSeq only
// runs after the first save released the lock (open.go holds m.mu.RLock for
// read-only config access but releases it before any other store call).
func (s *store) nextSeq(guildID, group string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	maxSeq := 0
	prefix := group + "-"
	if m, ok := s.tickets[guildID]; ok {
		for id := range m {
			if rest, found := strings.CutPrefix(id, prefix); found {
				if n, err := strconv.Atoi(rest); err == nil && n > maxSeq {
					maxSeq = n
				}
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(ticketsRoot(s.dataDir), guildID))
	if err == nil {
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".json")
			if rest, found := strings.CutPrefix(name, prefix); found {
				if n, err := strconv.Atoi(rest); err == nil && n > maxSeq {
					maxSeq = n
				}
			}
		}
	}
	for {
		cand := maxSeq + 1
		if !s.reserved[seqKey(guildID, group, cand)] {
			return cand
		}
		maxSeq = cand
	}
}

// reserveSeq atomically allocates AND reserves the next sequence number:
// reserved-but-not-yet-saved numbers are skipped by later allocations until
// releaseSeq (or a successful save) clears them. This closes the race where
// two concurrent opens both scan before either saves.
func (s *store) reserveSeq(guildID, group string) int {
	seq := s.nextSeq(guildID, group)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := seqKey(guildID, group, seq)
	for s.reserved[key] { // defensive: never hand out a live reservation
		s.mu.Unlock()
		seq = s.nextSeq(guildID, group)
		s.mu.Lock()
		key = seqKey(guildID, group, seq)
	}
	s.reserved[key] = true
	return seq
}

// releaseSeq drops a reservation (after save persisted it or the open failed).
func (s *store) releaseSeq(guildID, group string, seq int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reserved, seqKey(guildID, group, seq))
}

func seqKey(guildID, group string, seq int) string {
	return guildID + "|" + group + "|" + strconv.Itoa(seq)
}

// parseTicketID splits "<group>-<seq>" into its parts (guildID is not part
// of the ID itself; the caller supplies it).
func parseTicketID(id string) (group string, seq int, ok bool) {
	idx := strings.LastIndex(id, "-")
	if idx <= 0 || idx == len(id)-1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(id[idx+1:])
	if err != nil {
		return "", 0, false
	}
	return id[:idx], n, true
}

// validTicketID enforces the strict "<group>-<digits>" scheme at the trust
// boundary. Ticket IDs become file names (filepath.Join(ticketsRoot,
// guildID, ticketID+".json")), so anything outside this scheme — path
// separators, "..", whitespace, empty segments — is rejected before any
// filesystem access. Safety must not depend on router shape.
func validTicketID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			// allowed charset for group keys and digits
		default:
			return false // dots, slashes, spaces, unicode — everything else out
		}
	}
	group, seq, ok := parseTicketID(id)
	if !ok || group == "" {
		return false
	}
	digits := id[len(group)+1:]
	if len(digits) == 0 || len(digits) > 10 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	_ = seq
	return true
}

// pruneClosed removes closed tickets older than retentionDays (0 = disabled).
func (s *store) pruneClosed(retentionDays int) int {
	if retentionDays <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	s.mu.Lock()
	defer s.mu.Unlock()
	pruned := 0
	guilds, err := os.ReadDir(ticketsRoot(s.dataDir))
	if err != nil {
		return 0
	}
	for _, g := range guilds {
		if !g.IsDir() {
			continue
		}
		gid := g.Name()
		entries, _ := os.ReadDir(filepath.Join(ticketsRoot(s.dataDir), gid))
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || name == "index.json" || filepath.Ext(name) != ".json" {
				continue
			}
			tk, err := readTicketFile(filepath.Join(ticketsRoot(s.dataDir), gid, name))
			if err != nil || tk == nil || tk.Status != "closed" || tk.ClosedAt.After(cutoff) {
				continue
			}
			if os.Remove(filepath.Join(ticketsRoot(s.dataDir), gid, name)) == nil {
				delete(s.tickets[gid], tk.ID)
				pruned++
			}
		}
		_ = s.writeIndexLocked(gid)
	}
	return pruned
}

// flushAll is a no-op beyond ensuring indexes exist (every mutation already
// flushed); kept for OnUnload symmetry.
func (s *store) flushAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for gid := range s.tickets {
		if err := s.writeIndexLocked(gid); err != nil {
			return err
		}
	}
	return nil
}

func summaryOf(tk *modules.Ticket) modules.TicketSummary {
	return modules.TicketSummary{
		ID: tk.ID, Group: tk.Group, GuildID: tk.GuildID,
		OpenerID: tk.OpenerID, ClaimerID: tk.ClaimerID,
		Status: tk.Status, OpenedAt: tk.OpenedAt,
	}
}
