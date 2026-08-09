package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/graph"
)

// The CLI is built for programs; the web UI is built for people.
//
// That split is deliberate. A person asking "what can I run with vCenter 8.0U3k" gets a
// far better answer from the map than from a table, and an agent gets a far worse one
// from a table than from JSON it does not have to scrape. So every read command emits a
// versioned JSON envelope by default, and the old tables live behind --human.
//
// What an agent needs beyond "it is JSON":
//   - a schema name it can branch on, and a version it can pin
//   - the same envelope shape from every command, so one parser handles all of them
//   - errors as data, with a stable machine code, not prose on stderr
//   - exit codes that separate "you asked wrongly" from "the answer is no"
//   - a way to discover the surface without parsing help text (see `vkstack describe`)

// Mode reports the resolved output mode, for the error reporter in main.
//
// Flag parsing may not have happened — a malformed flag fails before PersistentPreRun —
// so this falls back to resolving from the raw arguments and environment.
func Mode() OutputMode {
	if g.mode != "" {
		return g.mode
	}
	for _, arg := range os.Args[1:] {
		if arg == "--human" {
			return OutputHuman
		}
	}
	return resolveMode()
}

// OutputMode is how a command should render its result.
type OutputMode string

const (
	// OutputJSON is the default: a versioned envelope, one object, on stdout.
	OutputJSON OutputMode = "json"
	// OutputHuman is the tables and prose this tool used to print by default.
	OutputHuman OutputMode = "human"
	// OutputCSV is for spreadsheets, on the commands that publish rows.
	OutputCSV OutputMode = "csv"
)

// SchemaVersion is bumped when the envelope's own shape changes, never for additive
// fields inside `data`. An agent can pin on this and on each payload's own schema name.
const SchemaVersion = 1

// envelope is what every JSON-emitting command writes. One shape for all of them, so a
// consumer writes one parser and branches on `schema`.
type envelope struct {
	// Schema names the payload, e.g. "vkstack.stack". Stable across releases.
	Schema string `json:"schema"`
	// Version is the payload's version, bumped on any breaking change to `data`.
	Version int `json:"version"`
	// Tool identifies the producer, so an agent that shells out to several tools can
	// tell where a blob came from.
	Tool string `json:"tool"`
	// Snapshot says which cache the answer came from. Compatibility answers are only
	// as current as the data behind them, and an agent cannot see the header a human
	// reads in the UI.
	Snapshot *snapshotJSON `json:"snapshot,omitempty"`
	Data     any           `json:"data"`
}

type snapshotJSON struct {
	FetchedAt string `json:"fetchedAt"`
	AgeHours  int    `json:"ageHours"`
	Stale     bool   `json:"stale"`
}

// emit writes a payload in whichever mode is active. Human and CSV rendering stay in the
// commands; this is the JSON path and the one place the envelope is built.
func emit(cmd *cobra.Command, schema string, version int, data any) error {
	env := envelope{
		Schema:   "vkstack." + schema,
		Version:  version,
		Tool:     "vkstack/" + strings.TrimPrefix(cmd.Root().Version, "v"),
		Snapshot: currentSnapshot(),
		Data:     data,
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// snapshotFn is set once the cache has been read, so emit can stamp the answer without
// every command threading the timestamp through by hand.
var snapshotFn func() *snapshotJSON

func currentSnapshot() *snapshotJSON {
	if snapshotFn == nil {
		return nil
	}
	return snapshotFn()
}

// --- errors as data --------------------------------------------------------

// Exit codes. An agent should be able to tell a bad question from a true "no" without
// reading any text.
const (
	ExitOK           = 0
	ExitError        = 1 // something went wrong
	ExitUsage        = 2 // the command was called wrongly
	ExitNotFound     = 3 // no such product, release or pair
	ExitAmbiguous    = 4 // the input matched several releases
	ExitNoData       = 5 // the cache is empty or the pair is unpublished
	ExitIncompatible = 6 // the question was valid and the answer is no
	ExitNoStack      = 7 // no complete stack exists for those pins
)

// CodedError is an error an agent can branch on. The message stays human-readable; the
// code is the part a program should read.
type CodedError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
	// Hint names the next thing to try, so an agent can recover without guessing.
	Hint string `json:"hint,omitempty"`
	Exit int    `json:"-"`
	// Silent marks a failure whose answer has already been written to stdout — a
	// `check` that came back incompatible is a result, not a malfunction, so the exit
	// code carries it and nothing is reported twice.
	Silent  bool `json:"-"`
	wrapped error
}

func (e *CodedError) Error() string { return e.Message }
func (e *CodedError) Unwrap() error { return e.wrapped }

func codedErr(code string, exit int, format string, args ...any) *CodedError {
	return &CodedError{Code: code, Message: fmt.Sprintf(format, args...), Exit: exit}
}

// classify turns an error from anywhere in the program into one an agent can act on.
//
// Most errors arrive as plain text from deep in the graph package. Rather than string
// matching at the boundary, the graph exports typed errors for the two cases a caller can
// actually do something about — a version that does not exist, and one that matches
// several — and those are mapped here.
func classify(err error) *CodedError {
	if err == nil {
		return nil
	}
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded
	}

	var ambiguous *graph.AmbiguousError
	if errors.As(err, &ambiguous) {
		return &CodedError{
			Code: "ambiguous_version", Exit: ExitAmbiguous, Message: err.Error(),
			Details: map[string]any{
				"product":    ambiguous.Product,
				"input":      ambiguous.Input,
				"candidates": ambiguous.Candidates,
			},
			Hint:    "pass one of the candidates verbatim",
			wrapped: err,
		}
	}

	var notFound *graph.NotFoundError
	if errors.As(err, &notFound) {
		return &CodedError{
			Code: "release_not_found", Exit: ExitNotFound, Message: err.Error(),
			Details: map[string]any{
				"product": notFound.Product,
				"input":   notFound.Input,
				"newest":  notFound.Newest,
			},
			Hint:    "list what exists with `vkstack releases <product>`",
			wrapped: err,
		}
	}

	msg := err.Error()
	switch {
	// Cobra reports a miscalled command as a plain error. Those are the caller's
	// mistake, not a failed query, and an agent needs to tell the two apart: it should
	// fix its call rather than conclude the answer is no.
	case isUsageError(msg):
		return &CodedError{
			Code: "usage", Exit: ExitUsage, Message: msg,
			Hint: "run `vkstack describe` for the exact command surface", wrapped: err,
		}
	case strings.Contains(msg, "no cached data"):
		return &CodedError{
			Code: "cache_empty", Exit: ExitNoData, Message: msg,
			Hint: "run `vkstack refresh` once; everything after that works offline", wrapped: err,
		}
	case strings.Contains(msg, "unknown product"):
		return &CodedError{
			Code: "unknown_product", Exit: ExitUsage, Message: msg,
			Hint: "product keys are vcenter, esx, supervisor, vks, vkr", wrapped: err,
		}
	}
	return &CodedError{Code: "error", Exit: ExitError, Message: msg, wrapped: err}
}

// isUsageError recognises the wordings cobra uses when a command is called wrongly.
//
// Matching on text is unpleasant, but this is the one boundary where the alternative —
// wrapping every command's argument validator and flag parser — would be more code and
// more places to forget. The strings are cobra's, and they are stable.
func isUsageError(msg string) bool {
	for _, marker := range []string{
		"accepts at most", "accepts between", "accepts ", "requires at least",
		"unknown flag", "unknown shorthand flag", "unknown command",
		"invalid argument", "flag needs an argument",
		"positional form is",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// ReportError renders a failure for whoever is watching: JSON on stderr for a program,
// a line of prose for a person. Returns the exit code to use.
//
// It goes to stderr in both modes so that stdout stays a single well-formed envelope, or
// nothing at all — an agent can parse stdout unconditionally without a success check.
func ReportError(err error, mode OutputMode) int {
	coded := classify(err)
	if coded == nil {
		return ExitOK
	}
	if coded.Silent {
		return coded.Exit
	}
	if mode == OutputHuman {
		fmt.Fprintf(os.Stderr, "error: %s\n", coded.Message)
		if coded.Hint != "" {
			fmt.Fprintf(os.Stderr, "hint: %s\n", coded.Hint)
		}
		return coded.Exit
	}
	writeErrorJSON(os.Stderr, coded)
	return coded.Exit
}

func writeErrorJSON(w io.Writer, coded *CodedError) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"schema":  "vkstack.error",
		"version": 1,
		"error":   coded,
	})
}
