package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
)

// The Pebble app speaks 2025-06-18. It reads only a single JSON response to
// each POST, never an SSE stream, and throws on a nextCursor in tools/list.
const protocolVersion = "2025-06-18"

var supportedVersions = map[string]bool{
	"2025-06-18": true,
	"2025-03-26": true,
}

// Given to the phone's on-device model, which picks a tool from what was
// said. It is small, so the choice has to be obvious.
const instructions = `Use capture when the user wants something written down: a note, a reminder, a todo or an idea. Use ask when the user asks a question and expects an answer: about their tickets, builds, pull requests, documentation or notes they captured earlier. Pass what the user said, unchanged.`

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`

	run func(ctx context.Context, text string) (string, error)
	arg string
}

func textSchema(arg, description string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			arg: map[string]any{"type": "string", "description": description},
		},
		"required": []string{arg},
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type server struct {
	token []byte
	tools []tool
}

func (s *server) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), s.token) == 1
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// There is no server-to-client stream to open with GET, and no session
	// to end with DELETE.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		// Batches were dropped from the protocol in 2025-06-18.
		writeJSON(w, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "expected one JSON-RPC message"}})
		return
	}
	// Notifications and responses get no reply.
	if len(req.ID) == 0 || req.Method == "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, rerr := s.dispatch(r.Context(), req)
	writeJSON(w, response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr})
}

func (s *server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := protocolVersion
		if supportedVersions[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ring-mcp", "version": "0.1.0"},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "arguments must be strings"}
		}
		for _, t := range s.tools {
			if t.Name != p.Name {
				continue
			}
			text := strings.TrimSpace(p.Arguments[t.arg])
			if text == "" {
				return toolResult("Nothing was said.", true), nil
			}
			out, err := t.run(ctx, text)
			if err != nil {
				log.Printf("%s: %v", t.Name, err)
				return toolResult(err.Error(), true), nil
			}
			return toolResult(out, false), nil
		}
		return nil, &rpcError{-32602, "unknown tool " + p.Name}
	default:
		return nil, &rpcError{-32601, "method not found: " + req.Method}
	}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("writing response: %v", err)
	}
}
