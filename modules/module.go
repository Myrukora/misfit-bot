package modules

import (
	"fmt"
	"plugin"
	"strings"
	"sync"

	"github.com/custombot/bot/commands"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
)

type Module interface {
	Name() string
	Version() string
	Description() string
	Author() string
	OnLoad(ctx *Context) error
	OnUnload() error
	Commands() []commands.Command
	SlashCommands() []commands.SlashCommand
	Dependencies() []string
}

// WebConfigurable is an OPTIONAL interface modules implement to expose
// editable settings to the dashboard. Modules are the single source of truth
// for what settings exist and exactly how each one is rendered — the dashboard
// never introspects module internals, it only renders whatever
// WebConfigSchema() returns. Existing modules that do not implement this
// interface are completely unaffected (the dashboard simply shows no config
// UI for them). The dashboard module itself implements it for self-config.
type WebConfigurable interface {
	// WebConfigSchema returns the ordered list of fields the dashboard renders.
	WebConfigSchema() []ConfigField
	// WebGetConfig returns current values (always strings). guildID "" means
	// global scope; a non-empty guildID means a guild-scoped snapshot.
	WebGetConfig(guildID string) (map[string]string, error)
	// WebSetConfig writes a single key. guildID "" is global scope. The module
	// owns all validation and may return an error string on bad input.
	WebSetConfig(guildID, key, value string) error
}

// ConfigField describes one dashboard-configurable setting. The module
// declares one of these per setting; the dashboard renders it purely from
// Type + Options + Min/Max/Step and does NO module-specific rendering. Every
// value is a string over the wire — the module parses ints/bools/etc. on its
// side (mirroring the bot's SetConfig(key,value string) convention).
type ConfigField struct {
	Key         string   // config key, e.g. "welcome_channel"
	Label       string   // human label shown in the UI
	Help        string   // description shown next to the field
	Type        string   // one of the FieldType* constants
	Options     []string // for select/multi (allowed choices)
	Min         *float64 // for number/range
	Max         *float64 // for number/range
	Step        *float64 // for number/range
	Placeholder string   // for text/select
	Scope       string   // "global" (owner/elevated) | "guild" (guild managers)
	GuildScoped bool     // true => field editable per-guild
}

// Field render types the dashboard understands. A module picks exactly one
// per ConfigField and implements no rendering logic of its own.
const (
	FieldTypeToggle   = "toggle"   // on/off switch (value "true"/"false")
	FieldTypeText     = "text"     // single-line free text
	FieldTypeTextarea = "textarea" // multi-line text
	FieldTypeNumber   = "number"   // numeric spinner; Min/Max enforced
	FieldTypeRange    = "range"    // slider; Min/Max/Step required
	FieldTypeSelect   = "select"   // single-select dropdown; Options required
	FieldTypeMulti    = "multi"    // multi-select; Options; value = newline- or comma-joined (newline keeps commas legal inside option values)
	FieldTypeSecret   = "secret"   // masked password; redacted "••••" on read
	FieldTypeChannel  = "channel"  // Discord channel picker (guild-scoped)
	FieldTypeRole     = "role"     // Discord role picker (guild-scoped)
	FieldTypeUser     = "user"     // Discord member picker (guild-scoped, capped)
)

// IsWebConfigurable type-asserts a module to WebConfigurable.
func IsWebConfigurable(m Module) (WebConfigurable, bool) {
	wc, ok := m.(WebConfigurable)
	return wc, ok
}

type Context struct {
	BotName      string
	OwnerID      string
	DataDir      string
	Logger       Logger
	Rest         rest.Rest
	Bot          commands.Interface
	Events       *EventHooks
	VoiceManager *VoiceManager
}

type EventHooks struct {
	mu sync.RWMutex

	OnMessageCreate         []func(event *events.MessageCreate)
	OnMessageUpdate         []func(event *events.MessageUpdate)
	OnMessageDelete         []func(event *events.MessageDelete)
	OnGuildMessageCreate    []func(event *events.GuildMessageCreate)
	OnGuildMessageUpdate    []func(event *events.GuildMessageUpdate)
	OnGuildMessageDelete    []func(event *events.GuildMessageDelete)
	OnGuildMemberJoin       []func(event *events.GuildMemberJoin)
	OnGuildMemberLeave      []func(event *events.GuildMemberLeave)
	OnGuildBan              []func(event *events.GuildBan)
	OnGuildUnban            []func(event *events.GuildUnban)
	OnGuildJoin             []func(event *events.GuildJoin)
	OnGuildLeave            []func(event *events.GuildLeave)
	OnPresenceUpdate        []func(event *events.PresenceUpdate)
	OnMessageReactionAdd    []func(event *events.MessageReactionAdd)
	OnMessageReactionRemove []func(event *events.MessageReactionRemove)
	OnVoiceStateUpdate      []func(event *events.GuildVoiceStateUpdate)
	OnComponentInteraction  []func(event *events.ComponentInteractionCreate)
	OnModalSubmit           []func(event *events.ModalSubmitInteractionCreate)
}

func NewEventHooks() *EventHooks {
	return &EventHooks{}
}

func (e *EventHooks) AddMessageCreate(h func(event *events.MessageCreate)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnMessageCreate = append(e.OnMessageCreate, h)
}

func (e *EventHooks) AddMessageUpdate(h func(event *events.MessageUpdate)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnMessageUpdate = append(e.OnMessageUpdate, h)
}

func (e *EventHooks) AddMessageDelete(h func(event *events.MessageDelete)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnMessageDelete = append(e.OnMessageDelete, h)
}

func (e *EventHooks) AddGuildMessageCreate(h func(event *events.GuildMessageCreate)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnGuildMessageCreate = append(e.OnGuildMessageCreate, h)
}

func (e *EventHooks) AddGuildMessageUpdate(h func(event *events.GuildMessageUpdate)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnGuildMessageUpdate = append(e.OnGuildMessageUpdate, h)
}

func (e *EventHooks) AddGuildMessageDelete(h func(event *events.GuildMessageDelete)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnGuildMessageDelete = append(e.OnGuildMessageDelete, h)
}

func (e *EventHooks) AddGuildMemberJoin(h func(event *events.GuildMemberJoin)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnGuildMemberJoin = append(e.OnGuildMemberJoin, h)
}

func (e *EventHooks) AddGuildMemberLeave(h func(event *events.GuildMemberLeave)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnGuildMemberLeave = append(e.OnGuildMemberLeave, h)
}

func (e *EventHooks) AddGuildBan(h func(event *events.GuildBan)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnGuildBan = append(e.OnGuildBan, h)
}

func (e *EventHooks) AddGuildUnban(h func(event *events.GuildUnban)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnGuildUnban = append(e.OnGuildUnban, h)
}

func (e *EventHooks) AddGuildJoin(h func(event *events.GuildJoin)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnGuildJoin = append(e.OnGuildJoin, h)
}

func (e *EventHooks) AddGuildLeave(h func(event *events.GuildLeave)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnGuildLeave = append(e.OnGuildLeave, h)
}

func (e *EventHooks) AddPresenceUpdate(h func(event *events.PresenceUpdate)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnPresenceUpdate = append(e.OnPresenceUpdate, h)
}

func (e *EventHooks) AddMessageReactionAdd(h func(event *events.MessageReactionAdd)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnMessageReactionAdd = append(e.OnMessageReactionAdd, h)
}

func (e *EventHooks) AddMessageReactionRemove(h func(event *events.MessageReactionRemove)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnMessageReactionRemove = append(e.OnMessageReactionRemove, h)
}

func (e *EventHooks) GetMessageCreateHandlers() []func(event *events.MessageCreate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.MessageCreate), len(e.OnMessageCreate))
	copy(h, e.OnMessageCreate)
	return h
}

func (e *EventHooks) GetMessageUpdateHandlers() []func(event *events.MessageUpdate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.MessageUpdate), len(e.OnMessageUpdate))
	copy(h, e.OnMessageUpdate)
	return h
}

func (e *EventHooks) GetMessageDeleteHandlers() []func(event *events.MessageDelete) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.MessageDelete), len(e.OnMessageDelete))
	copy(h, e.OnMessageDelete)
	return h
}

func (e *EventHooks) GetGuildMessageCreateHandlers() []func(event *events.GuildMessageCreate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildMessageCreate), len(e.OnGuildMessageCreate))
	copy(h, e.OnGuildMessageCreate)
	return h
}

func (e *EventHooks) GetGuildMessageUpdateHandlers() []func(event *events.GuildMessageUpdate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildMessageUpdate), len(e.OnGuildMessageUpdate))
	copy(h, e.OnGuildMessageUpdate)
	return h
}

func (e *EventHooks) GetGuildMessageDeleteHandlers() []func(event *events.GuildMessageDelete) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildMessageDelete), len(e.OnGuildMessageDelete))
	copy(h, e.OnGuildMessageDelete)
	return h
}

func (e *EventHooks) GetGuildMemberJoinHandlers() []func(event *events.GuildMemberJoin) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildMemberJoin), len(e.OnGuildMemberJoin))
	copy(h, e.OnGuildMemberJoin)
	return h
}

func (e *EventHooks) GetGuildMemberLeaveHandlers() []func(event *events.GuildMemberLeave) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildMemberLeave), len(e.OnGuildMemberLeave))
	copy(h, e.OnGuildMemberLeave)
	return h
}

func (e *EventHooks) GetGuildBanHandlers() []func(event *events.GuildBan) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildBan), len(e.OnGuildBan))
	copy(h, e.OnGuildBan)
	return h
}

func (e *EventHooks) GetGuildUnbanHandlers() []func(event *events.GuildUnban) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildUnban), len(e.OnGuildUnban))
	copy(h, e.OnGuildUnban)
	return h
}

func (e *EventHooks) GetGuildJoinHandlers() []func(event *events.GuildJoin) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildJoin), len(e.OnGuildJoin))
	copy(h, e.OnGuildJoin)
	return h
}

func (e *EventHooks) GetGuildLeaveHandlers() []func(event *events.GuildLeave) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildLeave), len(e.OnGuildLeave))
	copy(h, e.OnGuildLeave)
	return h
}

func (e *EventHooks) GetPresenceUpdateHandlers() []func(event *events.PresenceUpdate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.PresenceUpdate), len(e.OnPresenceUpdate))
	copy(h, e.OnPresenceUpdate)
	return h
}

func (e *EventHooks) GetMessageReactionAddHandlers() []func(event *events.MessageReactionAdd) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.MessageReactionAdd), len(e.OnMessageReactionAdd))
	copy(h, e.OnMessageReactionAdd)
	return h
}

func (e *EventHooks) GetMessageReactionRemoveHandlers() []func(event *events.MessageReactionRemove) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.MessageReactionRemove), len(e.OnMessageReactionRemove))
	copy(h, e.OnMessageReactionRemove)
	return h
}

func (e *EventHooks) AddVoiceStateUpdate(h func(event *events.GuildVoiceStateUpdate)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnVoiceStateUpdate = append(e.OnVoiceStateUpdate, h)
}

func (e *EventHooks) GetVoiceStateUpdateHandlers() []func(event *events.GuildVoiceStateUpdate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.GuildVoiceStateUpdate), len(e.OnVoiceStateUpdate))
	copy(h, e.OnVoiceStateUpdate)
	return h
}

func (e *EventHooks) AddComponentInteraction(h func(event *events.ComponentInteractionCreate)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnComponentInteraction = append(e.OnComponentInteraction, h)
}

func (e *EventHooks) GetComponentInteractionHandlers() []func(event *events.ComponentInteractionCreate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.ComponentInteractionCreate), len(e.OnComponentInteraction))
	copy(h, e.OnComponentInteraction)
	return h
}

func (e *EventHooks) AddModalSubmit(h func(event *events.ModalSubmitInteractionCreate)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.OnModalSubmit = append(e.OnModalSubmit, h)
}

func (e *EventHooks) GetModalSubmitHandlers() []func(event *events.ModalSubmitInteractionCreate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := make([]func(*events.ModalSubmitInteractionCreate), len(e.OnModalSubmit))
	copy(h, e.OnModalSubmit)
	return h
}

type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

type LoadedModule struct {
	Module       Module
	FilePath     string
	ModuleType   string // "go", "lua", "python"
	Plugin       *plugin.Plugin
	Hooks        *EventHooks
	LuaLoader    *LuaLoader
	PythonLoader *PythonLoader
}

type Manager struct {
	modules      map[string]*LoadedModule
	order        []string
	mu           sync.RWMutex
	moduleHooks  []*EventHooks
	hooksMu      sync.RWMutex
	luaLoader    *LuaLoader
	pythonLoader *PythonLoader
}

func NewManager() *Manager {
	return &Manager{
		modules: make(map[string]*LoadedModule),
		order:   make([]string, 0),
	}
}

// SetLuaLoader sets the Lua loader for the manager.
func (m *Manager) SetLuaLoader(loader *LuaLoader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.luaLoader = loader
}

// SetPythonLoader sets the Python loader for the manager.
func (m *Manager) SetPythonLoader(loader *PythonLoader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pythonLoader = loader
}

// DetectModuleType determines the module type from the file path.
func DetectModuleType(path string) string {
	if IsLuaModule(path) {
		return "lua"
	}
	if IsPythonModule(path) {
		return "python"
	}
	// Default to Go plugin for .so files
	return "go"
}

func (m *Manager) Load(path string, hooks *EventHooks) (Module, error) {
	moduleType := DetectModuleType(path)

	switch moduleType {
	case "lua":
		return m.loadLuaModule(path, hooks)
	case "python":
		return m.loadPythonModule(path, hooks)
	default:
		return m.loadGoPlugin(path, hooks)
	}
}

// loadGoPlugin loads a Go .so plugin module.
func (m *Manager) loadGoPlugin(path string, hooks *EventHooks) (Module, error) {
	p, err := plugin.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open plugin %s: %w", path, err)
	}

	sym, err := p.Lookup("New")
	if err != nil {
		return nil, fmt.Errorf("plugin %s does not export New(): %w", path, err)
	}

	newFunc, ok := sym.(func() Module)
	if !ok {
		return nil, fmt.Errorf("plugin %s New() has wrong signature", path)
	}

	mod := newFunc()
	name := mod.Name()

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.modules[name]; exists {
		return nil, fmt.Errorf("module %s is already loaded", name)
	}

	// Check dependencies
	if err := m.checkDependencies(mod); err != nil {
		return nil, fmt.Errorf("failed to load module %s: %w", name, err)
	}

	m.modules[name] = &LoadedModule{
		Module:     mod,
		FilePath:   path,
		ModuleType: "go",
		Plugin:     p,
		Hooks:      hooks,
	}
	m.order = append(m.order, name)

	if hooks != nil {
		m.AddModuleHooks(hooks)
	}

	return mod, nil
}

// loadLuaModule loads a Lua script module.
func (m *Manager) loadLuaModule(path string, hooks *EventHooks) (Module, error) {
	if m.luaLoader == nil {
		return nil, fmt.Errorf("Lua loader not configured")
	}

	mod, err := m.luaLoader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load Lua module %s: %w", path, err)
	}

	name := mod.Name()

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.modules[name]; exists {
		return nil, fmt.Errorf("module %s is already loaded", name)
	}

	// Check dependencies
	if err := m.checkDependencies(mod); err != nil {
		return nil, fmt.Errorf("failed to load module %s: %w", name, err)
	}

	m.modules[name] = &LoadedModule{
		Module:     mod,
		FilePath:   path,
		ModuleType: "lua",
		Hooks:      hooks,
	}
	m.order = append(m.order, name)

	if hooks != nil {
		m.AddModuleHooks(hooks)
	}

	return mod, nil
}

// loadPythonModule loads a Python module via the Python loader.
func (m *Manager) loadPythonModule(path string, hooks *EventHooks) (Module, error) {
	if m.pythonLoader == nil {
		return nil, fmt.Errorf("Python loader not configured")
	}

	mod, err := m.pythonLoader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load Python module %s: %w", path, err)
	}

	name := mod.Name()

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.modules[name]; exists {
		return nil, fmt.Errorf("module %s is already loaded", name)
	}

	// Check dependencies
	if err := m.checkDependencies(mod); err != nil {
		return nil, fmt.Errorf("failed to load module %s: %w", name, err)
	}

	m.modules[name] = &LoadedModule{
		Module:     mod,
		FilePath:   path,
		ModuleType: "python",
		Hooks:      hooks,
	}
	m.order = append(m.order, name)

	if hooks != nil {
		m.AddModuleHooks(hooks)
	}

	return mod, nil
}

func (m *Manager) Unload(name string) error {
	// Collect module info under lock
	m.mu.Lock()
	loaded, exists := m.modules[name]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("module %s is not loaded", name)
	}

	// Remove from maps under lock
	delete(m.modules, name)
	for i, n := range m.order {
		if n == name {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	mod := loaded.Module
	hooks := loaded.Hooks
	m.mu.Unlock()

	// Clean up hooks (separate lock)
	if hooks != nil {
		m.RemoveModuleHooks(hooks)
	}

	// Call OnUnload WITHOUT holding the lock to avoid deadlock
	unloadErr := mod.OnUnload()

	if unloadErr != nil {
		return fmt.Errorf("failed to unload module %s: %w", name, unloadErr)
	}
	return nil
}

func (m *Manager) Get(name string) (Module, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	loaded, exists := m.modules[name]
	if !exists {
		return nil, false
	}
	return loaded.Module, true
}

// GetInfo returns full metadata for a loaded module, including file path and type.
func (m *Manager) GetInfo(name string) (ModuleInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	loaded, exists := m.modules[name]
	if !exists {
		return ModuleInfo{}, false
	}
	return ModuleInfo{
		Name:        loaded.Module.Name(),
		Version:     loaded.Module.Version(),
		Description: loaded.Module.Description(),
		Author:      loaded.Module.Author(),
		Type:        loaded.ModuleType,
		Path:        loaded.FilePath,
		Loaded:      true,
	}, true
}

func (m *Manager) List() []ModuleInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	infos := make([]ModuleInfo, 0, len(m.order))
	for _, name := range m.order {
		loaded := m.modules[name]
		infos = append(infos, ModuleInfo{
			Name:        loaded.Module.Name(),
			Version:     loaded.Module.Version(),
			Description: loaded.Module.Description(),
			Author:      loaded.Module.Author(),
			Type:        loaded.ModuleType,
			Path:        loaded.FilePath,
			Loaded:      true,
		})
	}
	return infos
}

func (m *Manager) AllCommands() []commands.Command {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []commands.Command
	for _, name := range m.order {
		all = append(all, m.modules[name].Module.Commands()...)
	}
	return all
}

// AllCommandsByModule returns each loaded module's prefix commands grouped under
// the module's name, in load order. Modules with no commands are skipped. The
// bot's [p]help uses this to list each module's commands in a category named
// after the module itself (e.g. the cleanup module's commands appear under a
// "Cleanup" category).
func (m *Manager) AllCommandsByModule() []commands.ModuleCommands {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []commands.ModuleCommands
	for _, name := range m.order {
		cmds := m.modules[name].Module.Commands()
		if len(cmds) == 0 {
			continue
		}
		all = append(all, commands.ModuleCommands{Name: name, Commands: cmds})
	}
	return all
}

func (m *Manager) AllSlashCommands() []commands.SlashCommand {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []commands.SlashCommand
	for _, name := range m.order {
		all = append(all, m.modules[name].Module.SlashCommands()...)
	}
	return all
}

// checkDependencies validates that all dependencies for a module are loaded.
// Must be called with m.mu held.
func (m *Manager) checkDependencies(mod Module) error {
	deps := mod.Dependencies()
	if len(deps) == 0 {
		return nil
	}

	var missing []string
	for _, dep := range deps {
		if _, exists := m.modules[dep]; !exists {
			missing = append(missing, dep)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing dependencies: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (m *Manager) UnloadAll() error {
	// Collect all modules under lock
	m.mu.Lock()
	modulesToUnload := make(map[string]Module)
	for name, loaded := range m.modules {
		modulesToUnload[name] = loaded.Module
	}
	m.modules = make(map[string]*LoadedModule)
	m.order = nil
	m.mu.Unlock()

	// Clean up all hooks
	m.hooksMu.Lock()
	m.moduleHooks = nil
	m.hooksMu.Unlock()

	// Call OnUnload for each module WITHOUT holding the lock
	var errs []string
	for name, mod := range modulesToUnload {
		if err := mod.OnUnload(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to unload: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) GetNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, len(m.order))
	copy(names, m.order)
	return names
}

func (m *Manager) AddModuleHooks(h *EventHooks) {
	m.hooksMu.Lock()
	m.moduleHooks = append(m.moduleHooks, h)
	m.hooksMu.Unlock()
}

func (m *Manager) RemoveModuleHooks(h *EventHooks) {
	m.hooksMu.Lock()
	defer m.hooksMu.Unlock()
	for i, hooks := range m.moduleHooks {
		if hooks == h {
			m.moduleHooks = append(m.moduleHooks[:i], m.moduleHooks[i+1:]...)
			return
		}
	}
}

func (m *Manager) ListModuleHooks() []*EventHooks {
	m.hooksMu.RLock()
	defer m.hooksMu.RUnlock()
	out := make([]*EventHooks, len(m.moduleHooks))
	copy(out, m.moduleHooks)
	return out
}

type ModuleInfo struct {
	Name        string
	Version     string
	Description string
	Author      string
	Type        string // "go", "lua", "python"
	Path        string
	Loaded      bool
}
