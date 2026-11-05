package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
)

// rpcMessage is one JSON-RPC message from the client: a request when it
// has an ID, and otherwise a notification.
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// call is a request being answered. cancelled says the client withdrew
// it, which leaves it without a reply.
type call struct {
	cancel    context.CancelFunc
	cancelled bool
}

// serve answers the JSON-RPC requests read from in, each in a goroutine of
// its own, writing one reply per line to out. A notifications/cancelled
// cancels the context of the request it names, whose reply is then never
// written. A line that is not JSON gets a parse error (-32700), and the
// next line is read. When in ends it waits for the requests still running,
// and returns nil, or the first error writing a reply.
func serve(ctx context.Context, in io.Reader, out io.Writer, s *server) error {
	var (
		mu       sync.Mutex // guards encoder, calls and writeErr
		encoder  = json.NewEncoder(out)
		calls    = map[string]*call{}
		writeErr error
		running  sync.WaitGroup
	)
	// Messages are one per line, so that a malformed one is answered with
	// a parse error and the next line read.
	lines := bufio.NewReader(in)
	for {
		// At the end, a last line without its newline comes with io.EOF; it
		// is answered, and the next read returns nothing.
		line, readErr := lines.ReadBytes('\n')
		if len(line) == 0 && readErr != nil {
			running.Wait()
			if readErr != io.EOF {
				return readErr
			}
			mu.Lock()
			defer mu.Unlock()
			return writeErr
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			mu.Lock()
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}}); err != nil && writeErr == nil {
				writeErr = err
			}
			mu.Unlock()
			continue
		}
		if len(msg.ID) == 0 {
			if msg.Method == "notifications/cancelled" {
				var params struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				if json.Unmarshal(msg.Params, &params) == nil {
					mu.Lock()
					if c := calls[idKey(params.RequestID)]; c != nil {
						c.cancelled = true
						c.cancel()
					}
					mu.Unlock()
				}
			}
			continue
		}
		key := idKey(msg.ID)
		callCtx, cancel := context.WithCancel(ctx)
		c := &call{cancel: cancel}
		mu.Lock()
		calls[key] = c
		mu.Unlock()
		running.Add(1)
		go func() {
			defer running.Done()
			defer cancel()
			reply := s.answer(callCtx, msg)
			reply["jsonrpc"] = "2.0"
			reply["id"] = msg.ID
			mu.Lock()
			defer mu.Unlock()
			if calls[key] == c {
				delete(calls, key)
			}
			if c.cancelled {
				return
			}
			if err := encoder.Encode(reply); err != nil && writeErr == nil {
				writeErr = err
			}
		}()
	}
}

// idKey is a request ID in compact form, so that the ID a cancellation
// names matches the request's whatever its spacing.
func idKey(id json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, id) != nil {
		return string(id)
	}
	return b.String()
}

// answer returns the reply to one request, without its jsonrpc and id.
func (s *server) answer(ctx context.Context, msg rpcMessage) map[string]any {
	switch msg.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		version := "2025-06-18"
		if params.ProtocolVersion == "2024-11-05" || params.ProtocolVersion == "2025-03-26" {
			version = params.ProtocolVersion
		}
		return map[string]any{"result": map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "lima", "version": "1"}}}
	case "ping":
		return map[string]any{"result": map[string]any{}}
	case "tools/list":
		var list []any
		for _, t := range tools {
			list = append(list, map[string]any{"name": t.name, "description": t.description, "inputSchema": t.schema})
		}
		return map[string]any{"result": map[string]any{"tools": list}}
	case "tools/call":
		return map[string]any{"result": s.call(ctx, msg.Params)}
	}
	return map[string]any{"error": map[string]any{"code": -32601, "message": "method not found"}}
}
