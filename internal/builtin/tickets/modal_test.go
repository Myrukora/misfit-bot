package tickets

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/misfit/bot/modules"
)

// TestValidateQuestions pins the modal field limits: at most 5 questions, each
// with a non-empty label (<=45 runes), placeholder (<=100), value (<=4000),
// and a valid style.
func TestValidateQuestions(t *testing.T) {
	valid := []QuestionConfig{{Label: "What?", Style: "short", Required: true}}
	if err := validateQuestions(valid); err != nil {
		t.Fatalf("valid questions rejected: %v", err)
	}

	six := make([]QuestionConfig, 6)
	for i := range six {
		six[i] = QuestionConfig{Label: fmt.Sprintf("q%d", i)}
	}
	if err := validateQuestions(six); err == nil {
		t.Fatal("6 questions must be rejected")
	}

	if err := validateQuestions([]QuestionConfig{{Label: ""}}); err == nil {
		t.Fatal("empty label must be rejected")
	}
	if err := validateQuestions([]QuestionConfig{{Label: strings.Repeat("a", 46)}}); err == nil {
		t.Fatal("46-char label must be rejected")
	}
	if err := validateQuestions([]QuestionConfig{{Label: "ok", Style: "textarea"}}); err == nil {
		t.Fatal("bad style must be rejected")
	}
	if err := validateQuestions([]QuestionConfig{{Label: "ok", Placeholder: strings.Repeat("p", 101)}}); err == nil {
		t.Fatal("over-long placeholder must be rejected")
	}
	if err := validateQuestions([]QuestionConfig{{Label: "ok", Value: strings.Repeat("v", 4001)}}); err == nil {
		t.Fatal("over-long value must be rejected")
	}
}

// TestBuildOpenModal pins the modal shape: custom_id prefix, resolved +
// truncated title, and one labelled input per question (q0..qN-1) with the
// right style.
func TestBuildOpenModal(t *testing.T) {
	p := PanelConfig{
		Name: "staff_panel", TypeKey: "staff",
		ModalTitle: "Tell us what's up",
		Questions: []QuestionConfig{
			{Label: "What do you need?", Style: "short", Required: true},
			{Label: "Details", Style: "paragraph"},
		},
	}
	tt := TypeConfig{Key: "staff", Label: "Staff"}
	m := buildOpenModal(p, tt, "staff_panel")

	if m.CustomID != modalCustomIDPrefix+"staff_panel" {
		t.Fatalf("custom_id = %q, want %q", m.CustomID, modalCustomIDPrefix+"staff_panel")
	}
	if m.Title != "Tell us what's up" {
		t.Fatalf("title = %q, want resolved ModalTitle", m.Title)
	}
	if len(m.Components) != 2 {
		t.Fatalf("components = %d, want 2", len(m.Components))
	}
	for i, c := range m.Components {
		lc, ok := c.(discord.LabelComponent)
		if !ok {
			t.Fatalf("component %d is not a label: %T", i, c)
		}
		ti, ok := lc.Component.(discord.TextInputComponent)
		if !ok {
			t.Fatalf("component %d input is not a text input: %T", i, lc.Component)
		}
		if ti.CustomID != fmt.Sprintf("q%d", i) {
			t.Fatalf("input %d custom_id = %q, want q%d", i, ti.CustomID, i)
		}
	}
	if ti := m.Components[0].(discord.LabelComponent).Component.(discord.TextInputComponent); ti.Style != discord.TextInputStyleShort {
		t.Fatalf("input 0 style = %v, want short", ti.Style)
	}
	if ti := m.Components[1].(discord.LabelComponent).Component.(discord.TextInputComponent); ti.Style != discord.TextInputStyleParagraph {
		t.Fatalf("input 1 style = %v, want paragraph", ti.Style)
	}

	// Title falls back to the panel title, then the type label, and is
	// truncated to 45 runes.
	fb := buildOpenModal(PanelConfig{Name: "p", Questions: []QuestionConfig{{Label: "x"}}}, TypeConfig{Label: "Staff"}, "p")
	if fb.Title != "Staff" {
		t.Fatalf("fallback title = %q, want type label", fb.Title)
	}
	long := buildOpenModal(PanelConfig{Name: "p", ModalTitle: strings.Repeat("t", 60), Questions: []QuestionConfig{{Label: "x"}}}, TypeConfig{}, "p")
	if len([]rune(long.Title)) != 45 {
		t.Fatalf("title not truncated to 45: got %d runes", len([]rune(long.Title)))
	}
}

// componentEvent builds a button component interaction from a JSON blob.
func componentEvent(t *testing.T, customID, guildID string) *events.ComponentInteractionCreate {
	t.Helper()
	raw := fmt.Sprintf(`{"data":{"custom_id":%q,"component_type":2},"guild_id":%q}`, customID, guildID)
	var ci discord.ComponentInteraction
	if err := json.Unmarshal([]byte(raw), &ci); err != nil {
		t.Fatalf("unmarshal component interaction: %v", err)
	}
	return &events.ComponentInteractionCreate{ComponentInteraction: ci}
}

// TestHandlesRawComponent pins which open buttons the module wants raw
// (un-deferred): only open buttons whose panel has questions while modals are
// on. Claim/close and question-less panels stay deferred.
func TestHandlesRawComponent(t *testing.T) {
	const guildID = "111222333444555666"
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := &TicketsModule{
		ctx:    &modules.Context{Logger: testLogger{}},
		module: &ModuleConfig{Version: configVersion},
		store:  st,
		loaded: true,
		guilds: map[string]*Config{
			guildID: {
				Version: configVersion,
				Types:   map[string]*TypeConfig{},
				Panels: map[string]PanelConfig{
					"with_q": {Name: "with_q", TypeKey: "staff", Questions: []QuestionConfig{{Label: "Q"}}},
					"no_q":   {Name: "no_q", TypeKey: "staff"},
				},
			},
		},
	}

	cases := []struct {
		name     string
		customID string
		want     bool
	}{
		{"open with questions", "tickets:open:with_q", true},
		{"open without questions", "tickets:open:no_q", false},
		{"claim button", "tickets:claim:staff-0001", false},
		{"close button", "tickets:close:staff-0001", false},
		{"unknown panel", "tickets:open:missing", false},
	}
	for _, c := range cases {
		if got := m.HandlesRawComponent(componentEvent(t, c.customID, guildID)); got != c.want {
			t.Errorf("HandlesRawComponent(%s) = %v, want %v", c.name, got, c.want)
		}
	}

	// Modals off → even a panel with questions stays deferred.
	m.module.ModalsEnabled = new(false)
	if got := m.HandlesRawComponent(componentEvent(t, "tickets:open:with_q", guildID)); got {
		t.Error("modals off must not handle raw")
	}

	// Not loaded → never handle raw.
	m2 := &TicketsModule{ctx: &modules.Context{Logger: testLogger{}}}
	if got := m2.HandlesRawComponent(componentEvent(t, "tickets:open:with_q", guildID)); got {
		t.Error("unloaded module must not handle raw")
	}
}

// TestBuildAnswersContent pins the 2000-rune truncation: content that fits is
// returned whole; content that overflows is cut to 2000 runes plus an ellipsis.
func TestBuildAnswersContent(t *testing.T) {
	short := buildAnswersContent("Sam", []answer{{Label: "What?", Value: "help"}})
	if !strings.Contains(short, "**Answers from Sam**") || !strings.Contains(short, "help") {
		t.Fatalf("short content missing pieces: %q", short)
	}

	long := buildAnswersContent("Sam", []answer{{Label: "Details", Value: strings.Repeat("x", 3000)}})
	if len([]rune(long)) > 2000 {
		t.Fatalf("overflow content = %d runes, want ≤ 2000", len([]rune(long)))
	}
	if !strings.HasSuffix(long, "…") {
		t.Fatal("overflow content must end with an ellipsis")
	}
}
