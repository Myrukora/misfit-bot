package modules

// builtinNames is the single catalog of compiled-in feature modules. Every
// consumer (registration filter, module listings, load/unload guards, dashboard
// and MCP diagnostics) derives from this list so a new builtin cannot be
// half-registered.
var builtinNames = []string{
	"cleanup",
	"tickets",
	"imagefilter",
	"welcome",
	"moderation",
	"reactionroles",
	"leveling",
}

// BuiltinNames returns a copy of the compiled-in module names in catalog order.
func BuiltinNames() []string {
	out := make([]string, len(builtinNames))
	copy(out, builtinNames)
	return out
}

// IsBuiltin reports whether name is a compiled-in feature module.
func IsBuiltin(name string) bool {
	for _, n := range builtinNames {
		if n == name {
			return true
		}
	}
	return false
}
