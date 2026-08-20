package modules

import (
	"os/exec"
	"testing"

	"github.com/misfit/bot/logger"
)

// TestPythonReadyParsesWebConfig pins that a ready message carrying
// dashboard.py metadata (has_web_config + web_schema) is parsed into the
// module info the wrapper caches.
func TestPythonReadyParsesWebConfig(t *testing.T) {
	log, err := logger.New(t.TempDir(), "error", false)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })
	ipc := NewPythonIPC(&exec.Cmd{}, log)

	var got PythonReadyInfo
	ipc.onReady = func(info PythonReadyInfo) { got = info }

	ipc.handleMessage(map[string]interface{}{
		"type":           "ready",
		"name":           "pymod",
		"has_web_config": true,
		"web_schema": []interface{}{
			map[string]interface{}{
				"key": "enabled", "label": "Enabled", "type": "toggle",
				"scope": "global", "guild_scoped": false,
			},
			map[string]interface{}{
				"key": "tone", "label": "Tone", "type": "select",
				"scope": "guild", "guild_scoped": true,
				"options": []interface{}{"info", "fancy"},
			},
			map[string]interface{}{
				"key": "threshold", "type": "range", "min": float64(1), "max": float64(50), "step": float64(1),
			},
		},
	})

	if !got.HasWebConfig {
		t.Fatal("HasWebConfig = false, want true")
	}
	if len(got.WebSchema) != 3 {
		t.Fatalf("WebSchema len = %d, want 3", len(got.WebSchema))
	}
	f1 := got.WebSchema[1]
	if f1.Key != "tone" || !f1.GuildScoped || len(f1.Options) != 2 || f1.Options[1] != "fancy" {
		t.Errorf("tone field = %+v", f1)
	}
	f2 := got.WebSchema[2]
	if f2.Min == nil || *f2.Min != 1 || f2.Max == nil || *f2.Max != 50 || f2.Step == nil || *f2.Step != 1 {
		t.Errorf("threshold bounds = %+v", f2)
	}
}

// TestPythonReadyNoWebConfig pins the absence case: no dashboard.py => no
// integration metadata.
func TestPythonReadyNoWebConfig(t *testing.T) {
	log, err := logger.New(t.TempDir(), "error", false)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })
	ipc := NewPythonIPC(&exec.Cmd{}, log)

	var got PythonReadyInfo
	ipc.onReady = func(info PythonReadyInfo) { got = info }
	ipc.handleMessage(map[string]interface{}{"type": "ready", "name": "pymod"})

	if got.HasWebConfig {
		t.Fatal("HasWebConfig = true, want false (no dashboard.py)")
	}
	if len(got.WebSchema) != 0 {
		t.Fatalf("WebSchema = %v, want empty", got.WebSchema)
	}
}
