package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/search"
	"github.com/vkh/spacemosquito/internal/session"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logging"
)

// ProtocolVersion is the Streamable HTTP revision we advertise (wire: 2026-07-28 shape).
const ProtocolVersion = "2025-03-26"

type Server struct {
	db    store.Store
	pages pageStore
	store *session.Store
	cfg   *config.Config
	log   logging.Sugar
	// Search runs keyword/semantic/hybrid search; nil = keyword only.
	Search *search.Service
}

type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      interface{}     `json:"id,omitempty"`
}

type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

var ServerInstance *Server

func New(database store.Store, store *session.Store, cfg *config.Config, log logging.Sugar) *Server {
	server := &Server{
		db:    database,
		store: store,
		cfg:   cfg,
		log:   log,
	}
	ServerInstance = server
	return server
}

// HandleRequest serves Streamable HTTP on POST /mcp (JSON-RPC in, JSON out).
func (s *Server) HandleRequest(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r.Header.Get("Origin")) {
		writeJSONRPCError(w, http.StatusForbidden, nil, -32000, "forbidden origin", "")
		return
	}

	if r.Method == http.MethodGet {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	if !acceptOK(r.Header.Get("Accept")) {
		writeJSONRPCError(w, http.StatusBadRequest, nil, -32600, "Accept must include application/json and text/event-stream", "")
		return
	}

	if ver := r.Header.Get("MCP-Protocol-Version"); ver != "" && !protocolVersionOK(ver) {
		writeJSONRPCError(w, http.StatusBadRequest, nil, -32600, "unsupported MCP-Protocol-Version", ver)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONRPCError(w, http.StatusBadRequest, nil, -32700, "failed to read body", "")
		return
	}
	defer r.Body.Close()

	var req MCPRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPCError(w, http.StatusBadRequest, nil, -32700, "parse error", "invalid JSON")
		return
	}

	if s.log.Enabled() {
		s.log.Infow("MCP request received", "method", req.Method)
	}

	// Notifications (no id): 202 Accepted, empty body.
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := s.dispatch(&req)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) dispatch(req *MCPRequest) *MCPResponse {
	switch req.Method {
	case "initialize":
		return okResult(req.ID, map[string]interface{}{
			"protocolVersion": ProtocolVersion,
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "SpaceMosquito",
				"version": "1.0.0",
			},
		})
	case "notifications/initialized":
		return nil
	case "tools/list":
		return okResult(req.ID, map[string]interface{}{"tools": toolDefinitions()})
	case "tools/call":
		return s.handleToolsCall(req.Params, req.ID)
	case "ping":
		return okResult(req.ID, map[string]string{"status": "ok"})
	default:
		return rpcError(req.ID, -32601, "method not found", req.Method)
	}
}

func toolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "confluence_search",
			Description: "Search Confluence pages. Default mode is hybrid when semantic search is available: keyword (BM25, exact terms, IDs, titles) merged with meaning-based matches, so natural-language questions work. Results include confluence_id for use with confluence_get_page, match (keyword, semantic or both), heading_path of the matching section when the excerpt comes from it, and history.updated_by/updated_at (last Confluence update) when known.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Search query: keywords, an ID like CCP-761, or a question in natural language"},
					"space": {"type": "string", "description": "Optional space key to filter"},
					"limit": {"type": "integer", "description": "Max results (default 10)"},
					"mode": {"type": "string", "enum": ["hybrid", "keyword", "semantic"], "description": "hybrid (default when available), keyword (exact terms, all required) or semantic (meaning only)"}
				},
				"required": ["query"]
			}`),
		},
		{
			Name:        "confluence_list_spaces",
			Description: "List all crawled Confluence spaces",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {},
				"required": []
			}`),
		},
		{
			Name:        "confluence_list_space",
			Description: "List pages in a specific space with cursor pagination. Returns summary rows by default (no content). Use next_after_confluence_id from the response as after_confluence_id for the next page.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"space_key": {"type": "string", "description": "Space key"},
					"limit": {"type": "integer", "description": "Max results per page (default 50, max 200; max 50 when include_content is true)"},
					"after_confluence_id": {"type": "integer", "description": "Return pages with Confluence ID greater than this (exclusive). Omit for first page."},
					"include_content": {"type": "boolean", "description": "Include full page content in each row (default false). Prefer confluence_get_page for reading."}
				},
				"required": ["space_key"]
			}`),
		},
		{
			Name:        "confluence_get_page",
			Description: "Get a Confluence page by confluence_id (from search results or page URL). space_key is optional unless multiple spaces share the same ID. history gives who created and last updated the page in Confluence, and when (if known); updated_at is the local save time.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"confluence_id": {"type": "integer", "description": "Confluence page ID"},
					"space_key": {"type": "string", "description": "Optional. Required only when multiple spaces contain the same confluence_id."}
				},
				"required": ["confluence_id"]
			}`),
		},
	}
}

func (s *Server) handleToolsCall(params json.RawMessage, id interface{}) *MCPResponse {
	var callParams map[string]interface{}
	if err := json.Unmarshal(params, &callParams); err != nil {
		return rpcError(id, -32602, "invalid params", err.Error())
	}

	toolName, ok := callParams["name"].(string)
	if !ok {
		return rpcError(id, -32602, "invalid params", "tool name is required")
	}

	args, _ := callParams["arguments"].(map[string]interface{})
	if args == nil {
		args = make(map[string]interface{})
	}

	var result interface{}
	var err error
	start := time.Now()

	switch toolName {
	case "confluence_search":
		result, err = s.toolSearch(args)
	case "confluence_list_spaces":
		result, err = s.toolListSpaces()
	case "confluence_list_space":
		result, err = s.toolListSpace(args)
	case "confluence_get_page":
		result, err = s.toolGetPage(args)
	default:
		err = fmt.Errorf("unknown tool: %s", toolName)
	}

	if s.log.Enabled() {
		s.log.Infow("tool executed",
			"tool", toolName,
			"duration_ms", time.Since(start).Milliseconds(),
			"success", err == nil)
	}

	if err != nil {
		return okResult(id, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("Error: %v", err)},
			},
			"isError": true,
		})
	}

	resultStr, _ := json.MarshalIndent(result, "", "  ")
	return okResult(id, map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": string(resultStr)},
		},
	})
}

func (s *Server) toolSearch(args map[string]interface{}) (interface{}, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, fmt.Errorf("query is required")
	}

	space, _ := args["space"].(string)
	limit := 10
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	modeArg, _ := args["mode"].(string)
	mode, err := search.ParseMode(modeArg)
	if err != nil {
		return nil, err
	}
	var results []store.SearchResult
	if s.Search != nil {
		var res search.Response
		res, err = s.Search.Search(context.Background(), search.Request{Query: query, SpaceKey: space, Limit: limit, Mode: mode})
		results = res.Results
	} else {
		results, err = s.db.SearchPages(context.Background(), query, space, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	if results == nil {
		results = []store.SearchResult{}
	}
	return search.ToSearchHits(results, s.cfg.MCP.ExposeInternalIDs), nil
}

func (s *Server) toolListSpaces() (interface{}, error) {
	return s.db.ListSpaces(context.Background())
}

func (s *Server) toolListSpace(args map[string]interface{}) (interface{}, error) {
	parsed, err := parseListSpaceArgs(args)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	expose := s.cfg.MCP.ExposeInternalIDs
	if parsed.IncludeContent {
		pages, err := s.db.ListPages(ctx, parsed.SpaceKey, parsed.Limit+1, parsed.AfterConfluenceID)
		if err != nil {
			return nil, fmt.Errorf("list pages failed: %w", err)
		}
		return search.BuildListSpaceResultFromPages(parsed.SpaceKey, pages, parsed.Limit, expose), nil
	}
	summaries, err := s.db.ListPageSummaries(ctx, parsed.SpaceKey, parsed.Limit+1, parsed.AfterConfluenceID)
	if err != nil {
		return nil, fmt.Errorf("list pages failed: %w", err)
	}
	return search.BuildListSpaceResultFromSummaries(parsed.SpaceKey, summaries, parsed.Limit, expose), nil
}

func okResult(id interface{}, result interface{}) *MCPResponse {
	return &MCPResponse{JSONRPC: "2.0", Result: result, ID: id}
}

func rpcError(id interface{}, code int, message, data string) *MCPResponse {
	return &MCPResponse{
		JSONRPC: "2.0",
		Error:   &MCPError{Code: code, Message: message, Data: data},
		ID:      id,
	}
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeJSONRPCError(w http.ResponseWriter, httpStatus int, id interface{}, code int, message, data string) {
	writeJSON(w, httpStatus, rpcError(id, code, message, data))
}

// acceptOK requires application/json and text/event-stream (or */*) per Streamable HTTP.
func acceptOK(accept string) bool {
	a := strings.ToLower(accept)
	if strings.Contains(a, "*/*") {
		return true
	}
	return strings.Contains(a, "application/json") && strings.Contains(a, "text/event-stream")
}

func protocolVersionOK(ver string) bool {
	switch ver {
	case "2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28":
		return true
	default:
		return false
	}
}

// originAllowed implements DNS-rebinding protection: missing Origin is OK;
// when present, only loopback origins are accepted.
func originAllowed(origin string) bool {
	if origin == "" || origin == "null" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
