package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/warroyo/vkstack/internal/graph"
)

// An agent must be able to tell a bad question from a well-formed one whose answer is no,
// without reading any prose. These mappings are the contract that makes that possible, so
// they are pinned here rather than left to whatever the code happens to do.
func TestErrorsCarryMachineReadableCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
		exit int
	}{
		{
			name: "a version that does not exist",
			err:  &graph.NotFoundError{Product: "vCenter", Input: "99.9", Newest: "9.2.0.0"},
			code: "release_not_found", exit: ExitNotFound,
		},
		{
			name: "a prefix matching several releases",
			err: &graph.AmbiguousError{
				Product: "vCenter", Input: "9.1", Candidates: []string{"9.1.0.0", "9.1.1.0"},
			},
			code: "ambiguous_version", exit: ExitAmbiguous,
		},
		{
			name: "an empty cache",
			err:  errors.New("no cached data yet — run `vkstack refresh` first"),
			code: "cache_empty", exit: ExitNoData,
		},
		{
			name: "an unknown product key",
			err:  errors.New(`unknown product "vsphere" (want one of vcenter, esx)`),
			code: "unknown_product", exit: ExitUsage,
		},
		{
			name: "anything else",
			err:  errors.New("the disk caught fire"),
			code: "error", exit: ExitError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.err)
			if got.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Code, tc.code)
			}
			if got.Exit != tc.exit {
				t.Errorf("exit = %d, want %d", got.Exit, tc.exit)
			}
			if got.Message == "" {
				t.Error("an error must keep its human-readable message")
			}
		})
	}
}

// A coded error survives being wrapped: "no valid stack" must not decay into a generic
// failure just because something added context on the way up.
func TestCodedErrorsSurviveWrapping(t *testing.T) {
	inner := codedErr("no_valid_stack", ExitNoStack, "no valid stack found for those pins")
	wrapped := fmt.Errorf("solving: %w", inner)
	got := classify(wrapped)
	if got.Code != "no_valid_stack" || got.Exit != ExitNoStack {
		t.Errorf("wrapped coded error became %q/%d", got.Code, got.Exit)
	}
}

// `check` writes its verdict to stdout and then exits 6. Reporting the failure again on
// stderr would make an answer look like a malfunction.
func TestIncompatibleVerdictIsSilentButNonZero(t *testing.T) {
	err := &CodedError{
		Code: "stack_incompatible", Exit: ExitIncompatible, Silent: true,
		Message: "the pinned stack has incompatible pairs",
	}
	if got := ReportError(err, OutputJSON); got != ExitIncompatible {
		t.Errorf("exit = %d, want %d", got, ExitIncompatible)
	}
}

// The default is JSON because this CLI is for programs. Everything else is opt-in.
func TestOutputModeDefaultsToJSON(t *testing.T) {
	defer func(saved globals) { g = saved }(g)

	g = globals{}
	t.Setenv("VKSTACK_OUTPUT", "")
	if got := resolveMode(); got != OutputJSON {
		t.Errorf("default mode = %q, want json", got)
	}

	g = globals{humanOut: true}
	if got := resolveMode(); got != OutputHuman {
		t.Errorf("--human = %q, want human", got)
	}

	g = globals{}
	t.Setenv("VKSTACK_OUTPUT", "human")
	if got := resolveMode(); got != OutputHuman {
		t.Errorf("VKSTACK_OUTPUT=human = %q, want human", got)
	}

	// An explicit flag beats the environment, so a script cannot be broken by whatever
	// the surrounding shell happens to export.
	g = globals{jsonOut: true}
	if got := resolveMode(); got != OutputJSON {
		t.Errorf("--json with VKSTACK_OUTPUT=human = %q, want json", got)
	}
}
