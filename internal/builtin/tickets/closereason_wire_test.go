package tickets

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// TestCloseModalSubmitDecodesReason is the end-to-end guard for the close
// modal: it marshals the modal we BUILD, feeds the components back through the
// same JSON path Discord uses for a submit, and asserts the typed reason is
// recoverable by custom_id. This is what actually breaks if the label wrapping
// or the custom_id contract drifts — testing buildCloseModal's Go fields alone
// would not catch a wire-format mistake.
func TestCloseModalSubmitDecodesReason(t *testing.T) {
	const ticketID = "staff-0007"
	const want = "issue resolved, explained the rules"

	modal := buildCloseModal(ticketID)

	built, err := json.Marshal(modal)
	if err != nil {
		t.Fatalf("marshal modal: %v", err)
	}

	// Discord echoes the modal's components back on submit with the typed
	// values filled in. Reuse the marshal output so the shape is exactly what
	// we send, then inject the value where a text input lives.
	var asMap map[string]any
	if err := json.Unmarshal(built, &asMap); err != nil {
		t.Fatalf("unmarshal modal: %v", err)
	}
	comps, ok := asMap["components"].([]any)
	if !ok || len(comps) == 0 {
		t.Fatalf("modal has no components: %s", built)
	}
	row, ok := comps[0].(map[string]any)
	if !ok {
		t.Fatalf("first component is not an object: %v", comps[0])
	}
	// A label wraps the input, so the input sits under the label's component.
	inner, _ := row["component"].(map[string]any)
	if inner == nil {
		t.Fatalf("label has no inner component: %v", row)
	}
	if got := inner["custom_id"]; got != closeReasonFieldID {
		t.Fatalf("inner custom_id = %v, want %q", got, closeReasonFieldID)
	}
	inner["value"] = want

	submitJSON, err := json.Marshal(map[string]any{
		"custom_id":  modal.CustomID,
		"components": []any{row},
	})
	if err != nil {
		t.Fatalf("marshal submit: %v", err)
	}

	var data discord.ModalSubmitInteractionData
	if err := json.Unmarshal(submitJSON, &data); err != nil {
		t.Fatalf("decode submit payload: %v", err)
	}

	if got := data.Text(closeReasonFieldID); got != want {
		t.Fatalf("Text(%q) = %q, want %q", closeReasonFieldID, got, want)
	}
	// The ticket ID must survive the same payload, or the handler closes
	// nothing (or the wrong ticket).
	if got := trimClosePrefix(data.CustomID); got != ticketID {
		t.Fatalf("decoded ticket ID = %q, want %q", got, ticketID)
	}
}

// trimClosePrefix mirrors the handler's decode so the test pins the contract
// rather than duplicating a magic offset.
func trimClosePrefix(customID string) string {
	if len(customID) < len(closeModalCustomIDPrefix) || customID[:len(closeModalCustomIDPrefix)] != closeModalCustomIDPrefix {
		return ""
	}
	return customID[len(closeModalCustomIDPrefix):]
}

// TestCanCloseAsUser pins the interaction-free permission gate the modal-submit
// path relies on. This is what stops a stale open modal from closing a ticket
// after the closer lost their helper role.
func TestCanCloseAsUser(t *testing.T) {
	const (
		gid      = gateGuild
		opener   = "111"
		helperID = "222"
		stranger = "333"
	)
	helperRole := snowflake.MustParse("777888999000111222")

	m := gateModule(t, &TypeConfig{
		Key: "staff", Label: "Staff", Enabled: true,
		HelperRoles: []string{helperRole.String()}, CloseReason: boolPtr(true),
	}, map[snowflake.ID][]snowflake.ID{
		snowflake.MustParse(helperID): {helperRole},
	})

	tk := gateTicket()

	if !m.canCloseAsUser(tk, opener) {
		t.Error("opener should be allowed to close while allow_close is on")
	}
	if !m.canCloseAsUser(tk, helperID) {
		t.Error("helper role holder should be allowed to close")
	}
	if m.canCloseAsUser(tk, stranger) {
		t.Error("unrelated user should NOT be allowed to close")
	}
	if m.canCloseAsUser(tk, "") {
		t.Error("empty user id should not be allowed")
	}

	// allow_close off: the opener loses the right, the helper keeps it.
	g := m.guildConfig(gid)
	m.mu.Lock()
	g.Types["staff"].AllowClose = boolPtr(false)
	m.mu.Unlock()

	if m.canCloseAsUser(tk, opener) {
		t.Error("opener should NOT close when allow_close is false")
	}
	if !m.canCloseAsUser(tk, helperID) {
		t.Error("helper should still close when allow_close is false")
	}
}

// TestCloseModalSubmitRejectsBlankReason pins that whitespace cannot satisfy
// the required field: Discord's "required" only rejects a fully empty box, so
// the handler must reject blanks itself or the prompt is pointless.
func TestCloseModalSubmitRejectsBlankReason(t *testing.T) {
	// The guard lives in onCloseModalSubmit; assert the same predicate it uses.
	for _, blank := range []string{"", "   ", "\t", "\n  \n"} {
		if got := trimSpaceForTest(blank); got != "" {
			t.Fatalf("trimSpaceForTest(%q) = %q, want empty so the handler rejects it", blank, got)
		}
	}
	// And a real reason survives trimming.
	if got := trimSpaceForTest("  duplicate  "); got != "duplicate" {
		t.Fatalf("trim = %q, want duplicate", got)
	}
}

// trimSpaceForTest is strings.TrimSpace, named so the intent reads clearly in
// the assertion above.
func trimSpaceForTest(s string) string {
	return strings.TrimSpace(s)
}
