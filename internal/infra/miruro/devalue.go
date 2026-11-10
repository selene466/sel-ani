package miruro

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// SvelteKit route data (__data.json) is newline-delimited JSON. The first
// line holds one devalue-flattened value per route node; values the server
// streams later are placeholders ["Promise", id] whose results follow as
// {"type":"chunk","id":id,...} lines.
//
// devalue flattening: the value is an array; index 0 is the root. Arrays
// and object fields hold indices into it, negative indices are constants,
// and an array starting with a string is a typed value (["Date", ...]).

// page is a decoded __data.json response.
type page struct {
	Redirect string // set when the route answered with a redirect
	Nodes    []any  // per route node, nil when the node sent no data
}

// last returns the deepest route node that has data: the page itself.
func (p page) last() any {
	for i := len(p.Nodes) - 1; i >= 0; i-- {
		if p.Nodes[i] != nil {
			return p.Nodes[i]
		}
	}
	return nil
}

type rawLine struct {
	Type     string            `json:"type"`
	Location string            `json:"location"`
	Nodes    []json.RawMessage `json:"nodes"`
	ID       int               `json:"id"`
	Data     json.RawMessage   `json:"data"`
	Error    json.RawMessage   `json:"error"`
}

// maxDepth bounds recursion; real payloads nest a few dozen levels.
const maxDepth = 512

func decodePage(b []byte) (page, error) {
	var p page
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20)
	var head *rawLine
	chunks := map[int][]any{}
	failed := map[int]bool{}
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var l rawLine
		if err := json.Unmarshal(line, &l); err != nil {
			return p, fmt.Errorf("route data: %w", err)
		}
		switch l.Type {
		case "redirect":
			p.Redirect = l.Location
			return p, nil
		case "error":
			return p, fmt.Errorf("route data: server error %s", l.Error)
		case "data":
			if head == nil {
				head = &l
			}
		case "chunk":
			if l.Data == nil {
				failed[l.ID] = true
				continue
			}
			var arr []any
			if err := json.Unmarshal(l.Data, &arr); err != nil {
				return p, fmt.Errorf("route data chunk %d: %w", l.ID, err)
			}
			chunks[l.ID] = arr
		}
	}
	if err := sc.Err(); err != nil {
		return p, fmt.Errorf("route data: %w", err)
	}
	if head == nil {
		return p, errors.New("route data: no data line")
	}
	resolveChunk := func(id int) (any, error) {
		if failed[id] {
			return nil, fmt.Errorf("route data: streamed value %d failed", id)
		}
		arr, ok := chunks[id]
		if !ok {
			return nil, fmt.Errorf("route data: streamed value %d missing", id)
		}
		return unflatten(arr, nil)
	}
	for _, n := range head.Nodes {
		var node struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if string(n) == "null" || json.Unmarshal(n, &node) != nil || node.Type != "data" {
			p.Nodes = append(p.Nodes, nil)
			continue
		}
		var arr []any
		if err := json.Unmarshal(node.Data, &arr); err != nil {
			return p, fmt.Errorf("route data node: %w", err)
		}
		v, err := unflatten(arr, resolveChunk)
		if err != nil {
			return p, err
		}
		p.Nodes = append(p.Nodes, v)
	}
	return p, nil
}

// unflatten rebuilds the value at index 0. Promises are replaced by the
// chunk they resolved to; with chunk nil they become nil.
func unflatten(arr []any, chunk func(int) (any, error)) (any, error) {
	if len(arr) == 0 {
		return nil, nil
	}
	memo := map[int]any{}
	var hydrate func(i, depth int) (any, error)
	ref := func(v any, depth int) (any, error) {
		f, ok := v.(float64)
		if !ok || f != float64(int(f)) {
			return nil, fmt.Errorf("route data: bad reference %v", v)
		}
		return hydrate(int(f), depth+1)
	}
	hydrate = func(i, depth int) (any, error) {
		if i < 0 { // undefined, hole, NaN, ±Inf, -0: nothing we read
			return nil, nil
		}
		if i >= len(arr) {
			return nil, fmt.Errorf("route data: reference %d out of range", i)
		}
		if depth > maxDepth {
			return nil, errors.New("route data: nested too deeply")
		}
		if v, ok := memo[i]; ok {
			return v, nil
		}
		var out any
		switch v := arr[i].(type) {
		case []any:
			if len(v) > 0 {
				if tag, ok := v[0].(string); ok {
					var err error
					if out, err = typed(tag, v[1:], depth, ref, chunk); err != nil {
						return nil, err
					}
					break
				}
			}
			s := make([]any, len(v))
			for k, x := range v {
				var err error
				if s[k], err = ref(x, depth); err != nil {
					return nil, err
				}
			}
			out = s
		case map[string]any:
			m := make(map[string]any, len(v))
			for k, x := range v {
				var err error
				if m[k], err = ref(x, depth); err != nil {
					return nil, err
				}
			}
			out = m
		default:
			out = v
		}
		memo[i] = out
		return out, nil
	}
	return hydrate(0, 0)
}

func typed(tag string, args []any, depth int, ref func(any, int) (any, error), chunk func(int) (any, error)) (any, error) {
	switch tag {
	case "Promise":
		if chunk == nil || len(args) != 1 {
			return nil, nil
		}
		id, err := ref(args[0], depth)
		if err != nil {
			return nil, err
		}
		n, ok := id.(float64)
		if !ok {
			return nil, fmt.Errorf("route data: bad promise id %v", id)
		}
		return chunk(int(n))
	case "Date", "BigInt", "URL":
		if len(args) == 1 {
			if s, ok := args[0].(string); ok {
				return s, nil
			}
		}
	}
	return nil, nil // Map, Set, RegExp, ...: unused here
}

// into converts a decoded value to T through JSON.
func into[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}
