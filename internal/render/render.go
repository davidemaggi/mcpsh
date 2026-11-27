// Package render prints tool results for humans (text) and programs (JSON).
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Text writes the result content: text to out, or to errOut when the result is an error.
// Images, audio and binary resources are saved to temporary files and their paths printed.
// When there is no content but there is structuredContent, it is printed as indented JSON.
func Text(out, errOut io.Writer, res *mcp.CallToolResult, filePrefix string) error {
	w := out
	if res.IsError {
		w = errOut
	}
	wrote := false
	line := func(s string) {
		fmt.Fprint(w, s)
		if !strings.HasSuffix(s, "\n") {
			fmt.Fprintln(w)
		}
		wrote = true
	}
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			line(v.Text)
		case *mcp.ImageContent:
			p, err := saveBinary(filePrefix, v.MIMEType, v.Data)
			if err != nil {
				return err
			}
			line(fmt.Sprintf("[image %s saved to %s]", v.MIMEType, p))
		case *mcp.AudioContent:
			p, err := saveBinary(filePrefix, v.MIMEType, v.Data)
			if err != nil {
				return err
			}
			line(fmt.Sprintf("[audio %s saved to %s]", v.MIMEType, p))
		case *mcp.ResourceLink:
			desc := v.URI
			if v.Name != "" {
				desc = v.Name + " " + v.URI
			}
			line("[resource link: " + desc + "]")
		case *mcp.EmbeddedResource:
			r := v.Resource
			if r == nil {
				continue
			}
			if r.Blob != nil {
				p, err := saveBinary(filePrefix, r.MIMEType, r.Blob)
				if err != nil {
					return err
				}
				line(fmt.Sprintf("[resource %s saved to %s]", r.URI, p))
			} else {
				line(r.Text)
			}
		default:
			b, _ := json.Marshal(c)
			line(string(b))
		}
	}
	if !wrote && res.StructuredContent != nil {
		b, err := json.MarshalIndent(res.StructuredContent, "", "  ")
		if err != nil {
			return err
		}
		line(string(b))
	}
	return nil
}

// JSON writes the full MCP result (content, structuredContent, isError) as indented JSON.
func JSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func saveBinary(prefix, mimeType string, data []byte) (string, error) {
	ext := ".bin"
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		ext = exts[0]
		for _, e := range exts { // prefer the common spelling
			if e == ".png" || e == ".jpg" || e == ".gif" || e == ".webp" || e == ".mp3" || e == ".wav" || e == ".pdf" {
				ext = e
			}
		}
	}
	f, err := os.CreateTemp("", "mcpsh-"+sanitize(prefix)+"-*"+ext)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	return f.Name(), f.Close()
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}
