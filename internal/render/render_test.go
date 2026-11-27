package render

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestText(t *testing.T) {
	var out, errOut bytes.Buffer
	res := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "line one"},
		&mcp.ResourceLink{URI: "file:///x", Name: "x"},
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "mem://t", Text: "embedded"}},
		&mcp.AudioContent{Data: []byte("RIFF"), MIMEType: "audio/wav"},
	}}
	if err := Text(&out, &errOut, res, "s-t"); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"line one\n", "[resource link: x file:///x]", "embedded\n", "[audio audio/wav saved to "} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	i := strings.Index(s, "saved to ")
	p := strings.TrimSuffix(strings.TrimSpace(s[i+len("saved to "):]), "]")
	if !strings.HasSuffix(p, ".wav") {
		t.Errorf("audio file %q", p)
	}
	os.Remove(p)
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestTextErrorAndStructured(t *testing.T) {
	var out, errOut bytes.Buffer
	_ = Text(&out, &errOut, &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "bad"}}}, "x")
	if out.Len() != 0 || errOut.String() != "bad\n" {
		t.Errorf("isError: out=%q err=%q", out.String(), errOut.String())
	}
	out.Reset()
	_ = Text(&out, &errOut, &mcp.CallToolResult{StructuredContent: map[string]any{"a": 1}}, "x")
	if out.String() != "{\n  \"a\": 1\n}\n" {
		t.Errorf("structured = %q", out.String())
	}
}
