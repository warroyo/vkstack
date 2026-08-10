package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/api"
	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
)

// `vkstack mcp` serves these queries as MCP tools over stdio.
//
// The JSON-first CLI already gives an agent parseable answers, but it still has to know
// this binary exists, know the flag spelling, shell out, and interpret exit codes. MCP
// removes all four: the agent gets typed tools with declared inputs, and the answers come
// back as data on a channel it already speaks.
//
// The protocol here is hand-rolled — JSON-RPC 2.0 framed as newline-delimited JSON on
// stdin and stdout, which is what MCP's stdio transport is. That keeps this a single
// static binary with no cgo and no dependency on an SDK's release cadence, which is the
// same reason the rest of the tool is written the way it is.
//
// Anything written to stdout that is not a response corrupts the stream, so every
// diagnostic goes to stderr.

const (
	// mcpProtocolVersion is the revision this server implements. A client that asks for
	// a different one is answered with this, per the negotiation rule.
	mcpProtocolVersion = "2024-11-05"
	jsonRPCVersion     = "2.0"
)

func newMCPCmd() *cobra.Command {
	var refreshIfEmpty bool

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve compatibility queries as MCP tools over stdio",
		Long: `Serve this tool's queries to an agent over the Model Context Protocol.

Speaks JSON-RPC 2.0 on stdin and stdout, so it is wired up as a stdio MCP server:

  {"command": "vkstack", "args": ["mcp"]}

Every tool answers from the local cache and makes no network calls, so an agent cannot
be made to reach upstream by asking a question. Run ` + "`vkstack refresh`" + ` first, or
start with --refresh-if-empty so a fresh install populates itself once.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if refreshIfEmpty {
				if err := refreshEmptyCache(cmd); err != nil {
					// A failed pre-fetch is not fatal: the server still starts and every
					// tool answers "no cached data yet", which an agent can act on. Dying
					// here would leave the client with a transport error instead.
					fmt.Fprintf(cmd.ErrOrStderr(), "mcp: initial refresh failed: %v\n", err)
				}
			}
			srv := &mcpServer{
				in:  cmd.InOrStdin(),
				out: cmd.OutOrStdout(),
				log: cmd.ErrOrStderr(),
			}
			return srv.serve()
		},
	}
	// Opt in at launch, never mid-conversation. The operator starting the server decides
	// whether this host may call upstream; a tool call must never be able to trigger
	// network I/O as a side effect of a model asking a question.
	cmd.Flags().BoolVar(&refreshIfEmpty, "refresh-if-empty", false,
		"if the cache has no data, pull it once before serving (~1 min, ~40MB)")
	return cmd
}

// refreshEmptyCache populates a cold cache so a fresh install is usable on first call.
//
// It refreshes only when there is nothing at all. A stale cache is left alone: deciding
// that data is too old is the caller's judgement, and silently spending a minute on
// startup because a timestamp crossed a threshold is not a decision to make for them.
func refreshEmptyCache(cmd *cobra.Command) error {
	db, err := openCache()
	if err != nil {
		return err
	}
	defer db.Close()

	at, err := db.FetchedAt()
	if err != nil {
		return err
	}
	if !at.IsZero() {
		return nil
	}

	// Progress goes to stderr: stdout is the JSON-RPC stream and anything else on it
	// corrupts the protocol.
	fmt.Fprintln(cmd.ErrOrStderr(), "mcp: cache is empty, pulling the matrix once before serving")
	err = api.Refresh(cmd.Context(), api.New(g.apiBase, g.authKey), db, func(step, total int, msg string) {
		fmt.Fprintf(cmd.ErrOrStderr(), "mcp: [%d/%d] %s\n", step, total, msg)
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "mcp: cache populated")
	return nil
}

// --- JSON-RPC plumbing -----------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type mcpServer struct {
	in  io.Reader
	out io.Writer
	log io.Writer

	mu    sync.Mutex
	db    *store.DB
	graph *graph.Graph
}

func (s *mcpServer) serve() error {
	scanner := bufio.NewScanner(s.in)
	// Release lists and compatibility payloads are large; the default 64KB line limit
	// is not enough for a client that sends a big initialize.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.db != nil {
			s.db.Close()
		}
	}()

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.reply(rpcResponse{JSONRPC: jsonRPCVersion, Error: &rpcError{
				Code: -32700, Message: "parse error: " + err.Error(),
			}})
			continue
		}
		// A notification has no id and takes no response.
		if len(req.ID) == 0 {
			continue
		}
		s.reply(s.handle(req))
	}
	return scanner.Err()
}

func (s *mcpServer) reply(resp rpcResponse) {
	resp.JSONRPC = jsonRPCVersion
	body, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(s.log, "mcp: encoding response: %v\n", err)
		return
	}
	fmt.Fprintf(s.out, "%s\n", body)
}

func (s *mcpServer) handle(req rpcRequest) rpcResponse {
	resp := rpcResponse{ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "vkstack", "version": "1"},
			"instructions": "Compatibility across vCenter, ESX, vSphere Supervisor, VKS " +
				"and VKr, plus optional NSX and Avi Load Balancer, from a local mirror of " +
				"the Broadcom interoperability matrix. " +
				"Start with vkstack_stack to solve a whole valid stack from one pinned " +
				"version; vkstack_check validates a stack you already have. " +
				"NSX and Avi are optional and independent of each other: neither appears " +
				"in a stack unless it is pinned or named in vkstack_stack's `include`, " +
				"and a stack without them is a complete answer, not a partial one. " +
				"Asking for one never brings in the other — Avi on a distributed switch " +
				"with no NSX is an ordinary deployment. Answers are " +
				"never invented: pairs upstream does not publish are reported as inferred.",
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools()}
	case "tools/call":
		resp.Result = s.callTool(req.Params)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "unknown method: " + req.Method}
	}
	return resp
}

// --- the tools -------------------------------------------------------------

func mcpTools() []map[string]any {
	productEnum := make([]string, 0, len(model.Products))
	for _, p := range model.Products {
		productEnum = append(productEnum, p.Key)
	}
	product := map[string]any{
		"type": "string", "enum": productEnum,
		"description": "product key",
	}

	return []map[string]any{
		{
			"name": "vkstack_stack",
			"description": "Solve a whole valid stack from one or more pinned versions. " +
				"The headline query: given vCenter 8.0U3k, what Supervisor, VKS and VKr " +
				"can run with it. Returns the newest valid combination plus every " +
				"alternative each layer could still take. NSX and Avi are left out " +
				"unless pinned or listed in `include`.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pins": map[string]any{
						"type": "object",
						"description": "product key to version, e.g. " +
							`{"vcenter": "8.0U3k"}. One is enough. Pinning an optional ` +
							"product also opts it into the stack.",
						"additionalProperties": map[string]any{"type": "string"},
					},
					"hidePatches": map[string]any{
						"type": "boolean",
						"description": "hide patch releases from the solution (default false). " +
							"Patch releases are included by default because the patch letter " +
							"routinely decides compatibility: vCenter 8.0U3 takes Supervisor " +
							"1.26-1.28, 8.0U3k takes 1.31-1.33.",
					},
					"include": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string", "enum": optionalKeys()},
						"description": "optional products to include. Each is independent: " +
							`["avi"] solves a stack with Avi and no NSX, which is an ` +
							"ordinary distributed-switch deployment. Omit for the five " +
							"core products only.",
					},
				},
				"required": []string{"pins"},
			},
		},
		{
			"name": "vkstack_check",
			"description": "Validate a fully pinned stack pair by pair. Reports which " +
				"pairs are verified compatible, which are incompatible, and which " +
				"upstream never published — the last are not failures.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pins": map[string]any{
						"type":                 "object",
						"description":          "product key to version; pin at least two",
						"additionalProperties": map[string]any{"type": "string"},
					},
				},
				"required": []string{"pins"},
			},
		},
		{
			"name": "vkstack_compat",
			"description": "The raw pairwise answer for one release: everything the " +
				"matrix lists it against, grouped by product. Use vkstack_stack instead " +
				"when the question is what to run together.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"product":     product,
					"version":     map[string]any{"type": "string"},
					"hidePatches": map[string]any{"type": "boolean"},
				},
				"required": []string{"product", "version"},
			},
		},
		{
			"name":        "vkstack_releases",
			"description": "Every release of one product, newest last, with support phase.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"product":     product,
					"hidePatches": map[string]any{"type": "boolean"},
					"legacy": map[string]any{
						"type":        "boolean",
						"description": "include releases past General Support",
					},
				},
				"required": []string{"product"},
			},
		},
		{
			"name": "vkstack_products",
			"description": "The products in scope, their release counts, and which of " +
				"the 21 product pairs upstream actually publishes.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "vkstack_model",
			"description": "How the seven products — five core, plus optional NSX and " +
				"Avi — depend on each other, how each " +
				"relationship is known (published, inferred, or operational knowledge), " +
				"and what this tool does not know. Read this before reasoning about the " +
				"stack — it is the part that prevents wrong conclusions.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

func (s *mcpServer) callTool(raw json.RawMessage) map[string]any {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return toolError("bad arguments: " + err.Error())
	}

	var args struct {
		Pins        map[string]string `json:"pins"`
		Product     string            `json:"product"`
		Version     string            `json:"version"`
		HidePatches bool              `json:"hidePatches"`
		Patches     bool              `json:"patches"` // accepted and ignored: now the default
		Legacy      bool              `json:"legacy"`
		Include     []string          `json:"include"`
	}
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return toolError("bad arguments: " + err.Error())
		}
	}

	// The model needs no cache, so it stays answerable on a cold install.
	if params.Name == "vkstack_model" {
		return toolResult(map[string]any{"model": modelJSON(coverageFromCache())})
	}

	gr, err := s.loadGraph()
	if err != nil {
		return toolError(classify(err).Message)
	}

	switch params.Name {
	case "vkstack_products":
		return toolResult(map[string]any{
			"products": productsJSON(gr), "pairs": pairsJSON(gr),
		})

	case "vkstack_releases":
		p, ok := model.ByKey(args.Product)
		if !ok {
			return toolError(fmt.Sprintf("unknown product %q", args.Product))
		}
		var out []*graph.Release
		for _, r := range gr.ReleasesOf(p.ID) {
			if r.IsPatch() && args.HidePatches {
				continue
			}
			if !r.Phase().Supported() && !args.Legacy {
				continue
			}
			out = append(out, r)
		}
		return toolResult(map[string]any{"product": p.Key, "releases": out})

	case "vkstack_compat":
		r, err := gr.Resolve(args.Product, args.Version)
		if err != nil {
			return toolError(classify(err).Message)
		}
		groups := gr.CompatibleWith(r, graph.CompatOptions{HidePatches: args.HidePatches})
		return toolResult(map[string]any{
			"product": args.Product, "version": r.Raw, "groups": groups,
		})

	case "vkstack_check":
		pins, err := resolveNamedPins(gr, args.Pins)
		if err != nil {
			return toolError(classify(err).Message)
		}
		if len(pins) < 2 {
			return toolError("pin at least two products to check a stack")
		}
		return toolResult(checkJSON(gr.Check(pins)))

	case "vkstack_stack":
		pins, err := resolveNamedPins(gr, args.Pins)
		if err != nil {
			return toolError(classify(err).Message)
		}
		if len(pins) == 0 {
			return toolError("pin at least one version, e.g. {\"vcenter\": \"8.0U3k\"}")
		}
		include, err := resolveInclude(args.Include)
		if err != nil {
			return toolError(classify(err).Message)
		}
		opts := graph.StackOptions{Limit: 1, HidePatches: args.HidePatches, Include: include}
		stacks, failure := gr.Stacks(pins, opts)
		if len(stacks) == 0 {
			// A dead end is an answer. It comes back as a result rather than an error so
			// the agent can read why, instead of only that something failed.
			out := map[string]any{"ok": false, "reason": "no valid stack for those pins"}
			if failure != nil {
				out["blockedProduct"] = failure.BlockedProduct.Key
				out["against"] = failure.Against.Key
				out["pinConflict"] = failure.PinConflict
				out["neverListedTogether"] = failure.Unlisted
			}
			return toolResult(out)
		}
		return toolResult(buildRecommendationJSON(gr, stacks[0], gr.ViableOptions(pins, opts)))
	}
	return toolError("unknown tool: " + params.Name)
}

// resolveNamedPins turns {"vcenter": "8.0U3k"} into resolved releases, so a tool call
// accepts the same loose version strings a person would type.
func resolveNamedPins(gr *graph.Graph, pins map[string]string) (map[int]*graph.Release, error) {
	out := map[int]*graph.Release{}
	for key, version := range pins {
		if version == "" {
			continue
		}
		r, err := gr.Resolve(key, version)
		if err != nil {
			return nil, err
		}
		out[r.ProductID] = r
	}
	return out, nil
}

// loadGraph keeps one cache handle and one parsed graph for the life of the process,
// reloading only when the cache's timestamp moves — an agent may ask dozens of questions
// in a session, and reparsing the whole matrix for each would be wasteful.
func (s *mcpServer) loadGraph() (*graph.Graph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		db, err := openCache()
		if err != nil {
			return nil, err
		}
		s.db = db
	}
	at, err := s.db.FetchedAt()
	if err != nil {
		return nil, err
	}
	if at.IsZero() {
		return nil, fmt.Errorf("no cached data yet — run `vkstack refresh` first")
	}
	if s.graph != nil && s.graph.FetchedAt == at.UnixMilli() {
		return s.graph, nil
	}
	loaded, err := loadGraphFrom(s.db)
	if err != nil {
		return nil, err
	}
	s.graph = loaded
	return loaded, nil
}

// toolResult wraps a payload the way MCP expects: content blocks, plus the same object as
// structured content so a client that supports it does not have to re-parse the text.
func toolResult(data any) map[string]any {
	body, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return toolError("encoding result: " + err.Error())
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(body)}},
		"structuredContent": data,
		"isError":           false,
	}
}

// toolError reports a failed call as a tool result rather than a protocol error, which is
// what MCP asks for: the model should see what went wrong and be able to try again.
func toolError(message string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": message}},
		"isError": true,
	}
}
