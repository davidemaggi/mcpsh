// Package testserver is an MCP server with one tool per behaviour mcpsh must handle. It backs
// the integration tests (cmd/mcpsh-testserver runs it over stdio, HTTP or SSE).
package testserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New builds the server. exe is the test server binary, used to spawn grandchildren.
func New(exe string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "mcpsh-testserver", Version: "1.0.0"}, nil)

	type echoIn struct {
		Text  string `json:"text" jsonschema:"text to echo back"`
		Upper bool   `json:"upper,omitempty" jsonschema:"uppercase the text"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo the given text.\n\nUseful to check that mcpsh works."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			t := in.Text
			if in.Upper {
				t = strings.ToUpper(t)
			}
			return text(t), nil, nil
		})

	type addIn struct {
		A float64 `json:"a" jsonschema:"first addend"`
		B float64 `json:"b" jsonschema:"second addend"`
	}
	type addOut struct {
		Sum float64 `json:"sum"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "add", Description: "Add two numbers (structured output)."},
		func(_ context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
			return nil, addOut{Sum: in.A + in.B}, nil
		})

	s.AddTool(&mcp.Tool{
		Name:        "inspect_args",
		Description: "Return the received arguments as JSON.",
		InputSchema: json.RawMessage(`{
		  "type": "object",
		  "properties": {
		    "repo":     {"type": "string", "description": "Repository, owner/name"},
		    "state":    {"type": "string", "enum": ["open", "closed"], "default": "open"},
		    "labels":   {"type": "array", "items": {"type": "string"}, "description": "Labels to apply"},
		    "count":    {"type": "integer", "minimum": 1},
		    "ratio":    {"type": "number"},
		    "draft":    {"type": "boolean"},
		    "meta":     {"type": "object"},
		    "pageSize": {"type": "integer"},
		    "output":   {"type": "string", "description": "collides with the global --output flag"}
		  },
		  "required": ["repo"]
		}`),
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(string(req.Params.Arguments)), nil
	})

	type slowIn struct {
		MS int `json:"ms" jsonschema:"milliseconds to wait"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "slow", Description: "Wait, then answer."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in slowIn) (*mcp.CallToolResult, any, error) {
			select {
			case <-time.After(time.Duration(in.MS) * time.Millisecond):
				return text(fmt.Sprintf("waited %dms", in.MS)), nil, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		})

	type failIn struct {
		Message string `json:"message,omitempty"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Always fail with isError."},
		func(_ context.Context, _ *mcp.CallToolRequest, in failIn) (*mcp.CallToolResult, any, error) {
			m := in.Message
			if m == "" {
				m = "tool failed on purpose"
			}
			return nil, nil, errors.New(m)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "image", Description: "Return a 2x2 PNG image and some text."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "here is an image"},
				&mcp.ImageContent{Data: tinyPNG(), MIMEType: "image/png"},
			}}, nil, nil
		})

	destructive := true
	type destroyIn struct {
		Target string `json:"target"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "destroy", Description: "Pretend to delete something.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}},
		func(_ context.Context, _ *mcp.CallToolRequest, in destroyIn) (*mcp.CallToolResult, any, error) {
			return text("destroyed " + in.Target), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "crash", Description: "Exit the server process immediately."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			os.Exit(3)
			return nil, nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "spawn_child", Description: "Start a long-running grandchild process and return its PID."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			c := exec.Command(exe, "--sleep")
			if err := c.Start(); err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprint(c.Process.Pid)), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "pid", Description: "Return the server's process ID."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return text(fmt.Sprint(os.Getpid())), nil, nil
		})

	type envIn struct {
		Name string `json:"name"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "env", Description: "Return an environment variable of the server process."},
		func(_ context.Context, _ *mcp.CallToolRequest, in envIn) (*mcp.CallToolResult, any, error) {
			return text(os.Getenv(in.Name)), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "cwd", Description: "Return the server's working directory."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			wd, _ := os.Getwd()
			return text(wd), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "roots", Description: "Return the roots the client advertises."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			// Protocol 2026-07-28: server-to-client requests travel as multi round-trip
			// input requests (SEP-2322); the client answers and calls the tool again.
			if len(req.Params.InputResponses) == 0 {
				//lint:ignore SA1019 the test server exercises the deprecated roots feature on purpose
				return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"roots": &mcp.ListRootsParams{}}}, nil, nil
			}
			//lint:ignore SA1019 the test server exercises the deprecated roots feature on purpose
			res, ok := req.Params.InputResponses["roots"].(*mcp.ListRootsResult)
			if !ok {
				return nil, nil, errors.New("no roots in the input responses")
			}
			var uris []string
			for _, r := range res.Roots {
				uris = append(uris, r.URI)
			}
			return text(strings.Join(uris, "\n")), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "mutate", Description: "Add a tool named added_tool (sends tools/list_changed)."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			mcp.AddTool(s, &mcp.Tool{Name: "added_tool", Description: "Added at runtime."},
				func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
					return text("added"), nil, nil
				})
			return text("tool added"), nil, nil
		})
	return s
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func tinyPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
