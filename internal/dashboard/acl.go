package dashboard

import (
	"net/http"
	"sort"
	"time"

	"github.com/disgoorg/disgo/discord"
)

const (
	lvlOwner    = "owner"
	lvlElevated = "elevated"
	lvlStaff    = "staff"
	lvlRegular  = "regular"
)

var levelOrder = map[string]int{
	lvlRegular: 0, lvlStaff: 1, lvlElevated: 2, lvlOwner: 3,
}

const guildCacheTTL = 5 * time.Minute

// levelGEQ reports whether level a is at least level b (owner > elevated > staff > regular).
func levelGEQ(a, b string) bool { return levelOrder[a] >= levelOrder[b] }

// resolveLevel computes the dashboard RBAC tier for the logged-in user:
//
//	owner    — the bot owner (config owner_id)
//	elevated — a bot-level elevated user
//	staff    — manages >=1 guild the bot is in (guild owner or ManageGuild/Admin)
//	regular  — shares a mutual guild but manages none
func (m *DashboardModule) resolveLevel(us *userSession) string {
	if us == nil {
		return lvlRegular
	}
	id := us.userID.String()
	if m.bot.IsOwner(id) {
		return lvlOwner
	}
	if m.bot.IsElevated(id) {
		return lvlElevated
	}
	// NOTE: must NOT call manageableGuildIDs here — it checks owner/elevated
	// itself, and calling resolveLevel from it would recurse forever for
	// staff/regular sessions (staff/regular check each other without a base
	// case). managedMutualGuildIDs never consults resolveLevel.
	if len(m.managedMutualGuildIDs(us)) > 0 {
		return lvlStaff
	}
	return lvlRegular
}

// botGuildIDs returns the set of guild IDs the bot is in (from the cache).
func (m *DashboardModule) botGuildIDs() map[string]bool {
	out := map[string]bool{}
	if m.client == nil {
		return out
	}
	for g := range m.client.Caches.Guilds() {
		out[g.ID.String()] = true
	}
	return out
}

// allBotGuildList returns every guild ID the bot is in.
func (m *DashboardModule) allBotGuildList() []string {
	out := make([]string, 0, len(m.botGuildIDs()))
	for id := range m.botGuildIDs() {
		if m.allowed(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// allowed enforces the optional allowed_guilds allowlist (cfg read under m.mu
// — the allowlist is swapped from gateway goroutines via cmdDashboardSet).
func (m *DashboardModule) allowed(guildID string) bool {
	m.mu.Lock()
	list := m.cfg.AllowedGuilds
	m.mu.Unlock()
	if len(list) == 0 {
		return true
	}
	for _, a := range list {
		if a == guildID {
			return true
		}
	}
	return false
}

// refreshGuilds re-fetches the user's OAuth guilds occasionally to limit API
// calls while keeping dynamic membership roughly current. The session mutex
// is only held for the cache check and the store — never across the OAuth
// network call (a slow Discord response would otherwise stall every page for
// that user, since userOAuthGuilds/baseData wait on the same lock).
func (m *DashboardModule) refreshGuilds(us *userSession) {
	if us == nil {
		return
	}
	oa := m.oauthClient()
	if oa == nil {
		return
	}
	us.mu.Lock()
	fresh := time.Since(us.guildsAt) < guildCacheTTL && len(us.oauthGuilds) > 0
	us.mu.Unlock()
	if fresh {
		return
	}
	guilds, err := oa.GetGuilds(us.oauth)
	us.mu.Lock()
	defer us.mu.Unlock()
	if err != nil {
		// Keep the stale cache (if any); log so a broken token is diagnosable.
		m.logger.Warn("dashboard: refresh OAuth guilds failed: %v", err)
		return
	}
	us.oauthGuilds = guilds
	us.guildsAt = time.Now()
}

// userOAuthGuilds returns the session's cached OAuth guild list.
func (m *DashboardModule) userOAuthGuilds(us *userSession) []discord.OAuth2Guild {
	if us == nil {
		return nil
	}
	us.mu.Lock()
	defer us.mu.Unlock()
	return us.oauthGuilds
}

// mutualGuildIDs returns the user's guilds that the bot is also in.
func (m *DashboardModule) mutualGuildIDs(us *userSession) []string {
	if us == nil {
		return nil
	}
	m.refreshGuilds(us)
	botSet := m.botGuildIDs()
	var out []string
	for _, g := range m.userOAuthGuilds(us) {
		if botSet[g.ID.String()] && m.allowed(g.ID.String()) {
			out = append(out, g.ID.String())
		}
	}
	sort.Strings(out)
	return out
}

// manageableGuildIDs returns the guilds this user can manage from the dashboard:
// owner/elevated manage every bot guild; otherwise the subset of mutual guilds
// where the user owns the guild or has ManageGuild/Administrator. It never
// calls resolveLevel (see resolveLevel's NOTE) — owner/elevated are tested
// directly so the two functions cannot recurse into each other.
func (m *DashboardModule) manageableGuildIDs(us *userSession) []string {
	if us == nil {
		return nil
	}
	id := us.userID.String()
	if m.bot.IsOwner(id) || m.bot.IsElevated(id) {
		return m.allBotGuildList()
	}
	return m.managedMutualGuildIDs(us)
}

// managedMutualGuildIDs returns the mutual guilds where the user owns the
// guild or holds ManageGuild/Administrator. It never consults resolveLevel,
// so it is safe to call from resolveLevel itself.
func (m *DashboardModule) managedMutualGuildIDs(us *userSession) []string {
	if us == nil {
		return nil
	}
	m.refreshGuilds(us)
	botSet := m.botGuildIDs()
	var out []string
	for _, g := range m.userOAuthGuilds(us) {
		if !botSet[g.ID.String()] || !m.allowed(g.ID.String()) {
			continue
		}
		if g.Owner || g.Permissions.Has(discord.PermissionAdministrator) || g.Permissions.Has(discord.PermissionManageGuild) {
			out = append(out, g.ID.String())
		}
	}
	sort.Strings(out)
	return out
}

// canManageGuild is the per-guild authorization check used by guild pages and
// guild-scoped config writes.
func (m *DashboardModule) canManageGuild(us *userSession, guildID string) bool {
	if us == nil {
		return false
	}
	level := m.resolveLevel(us)
	if level == lvlOwner || level == lvlElevated {
		return m.allowed(guildID)
	}
	for _, id := range m.manageableGuildIDs(us) {
		if id == guildID {
			return true
		}
	}
	return false
}

// ── Route guards ─────────────────────────────────────────────────────────

func (m *DashboardModule) requireAuthed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sessionOf(r) == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireMin denies (403) below the minimum tier. Staff+ for guild pages,
// elevated+ for owner/elevated-only endpoints.
func (m *DashboardModule) requireMin(min string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		us := sessionOf(r)
		if us == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !levelGEQ(m.resolveLevel(us), min) {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		next(w, r)
	}
}

// requireOwner wraps a handler so only owner/elevated users may call it.
func (m *DashboardModule) requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return m.requireMin(lvlElevated, next) // owner OR elevated
}

// requireGuild allows owner/elevated or a staff member who can manage the guild.
func (m *DashboardModule) requireGuild(guildID string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		us := sessionOf(r)
		if us == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !m.canManageGuild(us, guildID) {
			http.NotFound(w, r) // don't leak existence
			return
		}
		next(w, r)
	}
}
