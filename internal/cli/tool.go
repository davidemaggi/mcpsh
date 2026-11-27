package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/render"
	"github.com/davidemaggi/mcpsh/internal/schema"
)

// toolCmd builds "mcpsh <server> <tool>" with flags generated from the input schema.
func (a *App) toolCmd(rs *config.Resolved, t *mcp.Tool) (*cobra.Command, error) {
	spec, err := schema.Build(t.InputSchema)
	if err != nil {
		return nil, err
	}
	var (
		jsonArg    string
		showSchema bool
	)
	hint := fmt.Sprintf("see mcpsh %s %s --help", rs.Name, t.Name)
	cmd := &cobra.Command{
		Use:   t.Name,
		Short: firstLine(t.Description),
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usagef(hint, fmt.Sprintf("unexpected argument %q: parameters are passed as flags (--name value)", args[0]))
			}
			return nil
		},
	}
	if alias := schema.Kebab(t.Name); alias != t.Name {
		cmd.Aliases = []string{alias}
	}
	binding := spec.Bind(cmd.Flags())
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if showSchema {
			return writeJSON(a.Stdout, map[string]any{"inputSchema": t.InputSchema, "outputSchema": t.OutputSchema})
		}
		return a.runTool(cmd.Context(), rs, t, spec, binding, jsonArg, hint)
	}
	cmd.Flags().StringVar(&jsonArg, "json", "", "arguments as a JSON object: '{...}', @file.json, or - for stdin (flags override its keys)")
	cmd.Flags().BoolVar(&showSchema, "schema", false, "print the tool's input and output JSON Schema and exit")
	cmd.SetHelpFunc(func(c *cobra.Command, _ []string) { a.toolHelp(c, rs, t, spec) })
	return cmd, nil
}

func (a *App) runTool(ctx context.Context, rs *config.Resolved, t *mcp.Tool, spec *schema.Spec, binding *schema.Binding, jsonArg, hint string) error {
	base, err := a.readJSONArg(jsonArg)
	if err != nil {
		return usagef(hint, err.Error())
	}
	args := binding.Args(base)
	if miss := spec.Missing(args); len(miss) > 0 {
		names := make([]string, len(miss))
		for i, p := range miss {
			names[i] = "--" + p.Flag
		}
		word := "parameter"
		if len(names) > 1 {
			word = "parameters"
		}
		return usagef(hint, fmt.Sprintf("missing required %s %s", word, strings.Join(names, ", ")))
	}
	if err := spec.Validate(args); err != nil {
		return usagef(hint, "invalid arguments: "+err.Error())
	}
	if a.isDestructive(rs, t) && !a.yes {
		if err := a.confirm(rs, t); err != nil {
			return err
		}
	}

	cctx, cancel := context.WithTimeout(ctx, a.callTimeout(rs))
	defer cancel()
	res, err := a.engine().CallTool(cctx, rs, t.Name, args)
	if err != nil {
		return err
	}
	if a.jsonOutput() {
		if err := render.JSON(a.Stdout, res); err != nil {
			return err
		}
	} else if err := render.Text(a.Stdout, a.Stderr, res, rs.Name+"-"+t.Name); err != nil {
		return err
	}
	if res.IsError {
		return errToolFailed
	}
	return nil
}

// readJSONArg parses --json: inline JSON, @file, or - for stdin.
func (a *App) readJSONArg(v string) (map[string]any, error) {
	if v == "" {
		return map[string]any{}, nil
	}
	var data []byte
	var err error
	src := "--json"
	switch {
	case v == "-":
		src = "stdin"
		data, err = io.ReadAll(a.Stdin)
	case strings.HasPrefix(v, "@"):
		src = v[1:]
		data, err = os.ReadFile(v[1:])
	default:
		data = []byte(v)
	}
	if err != nil {
		return nil, fmt.Errorf("reading --json from %s: %v", src, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("--json from %s must be a JSON object: %v", src, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func (a *App) confirm(rs *config.Resolved, t *mcp.Tool) error {
	msg := fmt.Sprintf("tool %q of server %q is marked as destructive", t.Name, rs.Name)
	if !a.Interactive {
		return usagef("", msg+"; re-run with --yes to confirm")
	}
	fmt.Fprintf(a.Stderr, "%s. Run it? [y/N] ", msg)
	line, _ := bufio.NewReader(a.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return usagef("", "cancelled")
}

func (a *App) toolHelp(c *cobra.Command, rs *config.Resolved, t *mcp.Tool, spec *schema.Spec) {
	w := c.OutOrStdout()
	fmt.Fprintf(w, "Usage: mcpsh %s %s [--param value ...] [--json '{...}']\n", rs.Name, t.Name)
	if len(c.Aliases) > 0 {
		fmt.Fprintf(w, "Alias: %s\n", strings.Join(c.Aliases, ", "))
	}
	if t.Title != "" {
		fmt.Fprintf(w, "\n%s\n", t.Title)
	}
	if d := strings.TrimSpace(t.Description); d != "" {
		fmt.Fprintf(w, "\n%s\n", d)
	}
	var notes []string
	if a.isDestructive(rs, t) {
		notes = append(notes, "destructive (needs --yes)")
	}
	if t.Annotations != nil {
		if t.Annotations.ReadOnlyHint {
			notes = append(notes, "read-only")
		}
		if t.Annotations.IdempotentHint {
			notes = append(notes, "idempotent")
		}
	}
	if len(notes) > 0 {
		fmt.Fprintf(w, "\nAnnotations: %s\n", strings.Join(notes, ", "))
	}
	if len(spec.Params) > 0 {
		fmt.Fprintf(w, "\nParameters:\n%s", spec.FlagUsages())
	} else {
		fmt.Fprintln(w, "\nParameters: none")
	}
	fmt.Fprint(w, `
Flags:
  --json json|@file|-   arguments as a JSON object; flags override its keys
  --schema              print the input/output JSON Schema and exit
  -o, --output text|json
  --timeout duration    call timeout (default from config, else 60s)
  -y, --yes             confirm a destructive tool
`)
}

func writeJSON(w io.Writer, v any) error { return render.JSON(w, v) }
