// A scripted OpenAI-compatible endpoint, for recording the README demo
// without spending money or putting a private gateway's name in a public GIF.
//
// Only the model's own words are scripted. uhai really runs the tool calls
// this returns, so the file contents, the diff in the permission question and
// the token counts on screen are all real.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const model = "demo-model"

// turn is one scripted reply: either some text, or tools to call.
type turn struct {
	text  string
	calls []call
}

type call struct {
	name string
	args string
}

// script is answered in order. uhai sends one request per iteration of its
// tool loop, so this is the conversation read top to bottom.
var script = []turn{
	{calls: []call{
		{"list_directory", `{"path":"cmd/uhai"}`},
		{"read_file", `{"path":"cmd/uhai/main.go"}`},
	}},
	{text: "`cmd/uhai/main.go` is the whole entry point, and it is short on " +
		"purpose: it parses flags and hands off. Every flag maps to one call " +
		"into `internal/orchestrator`, which is the only place that says " +
		"which parts make up a running uhai.\n\n" +
		"There is no logic here to get wrong — which is why the file can be " +
		"read in a minute."},
	{calls: []call{
		{"edit_file", `{"path":"cmd/uhai/main.go","old":"func main() {","new":"// main parses the flags and hands off; every branch below is one call\n// into internal/orchestrator, which owns what a running uhai is made of.\nfunc main() {"}`},
	}},
	{text: "Added. The comment says why the file is short rather than what " +
		"`main` does, which the code below it already says."},
}

var (
	mu sync.Mutex
	at int
)

func main() {
	addr := ":8977"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": model}},
		})
	})

	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := at
		if at < len(script)-1 {
			at++
		}
		mu.Unlock()

		flush, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no streaming here", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		send := func(delta any, finish string) {
			chunk := map[string]any{
				"id":    "chatcmpl-demo",
				"model": model,
				"choices": []map[string]any{{
					"index": 0, "delta": delta, "finish_reason": finish,
				}},
			}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flush.Flush()
		}

		t := script[i]
		if len(t.calls) > 0 {
			for n, c := range t.calls {
				send(map[string]any{"tool_calls": []map[string]any{{
					"index": n,
					"id":    fmt.Sprintf("call_%d_%d", i, n),
					"type":  "function",
					"function": map[string]string{
						"name": c.name, "arguments": c.args,
					},
				}}}, "")
				time.Sleep(250 * time.Millisecond)
			}
			send(map[string]any{}, "tool_calls")
		} else {
			// Word by word, because a demo of a streaming agent that arrives
			// all at once looks like a screenshot.
			for _, word := range strings.SplitAfter(t.text, " ") {
				send(map[string]any{"content": word}, "")
				time.Sleep(18 * time.Millisecond)
			}
			send(map[string]any{}, "stop")
		}

		// The usage frame is what puts token counts in the status row.
		usage, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-demo", "model": model, "choices": []any{},
			"usage": map[string]int{
				"prompt_tokens": 4820 + i*900, "completion_tokens": 120 + i*40,
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", usage)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flush.Flush()
	})

	log.Printf("scripted endpoint on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
