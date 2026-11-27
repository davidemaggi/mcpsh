package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/render"
)

// requestCtx bounds a resource or prompt request like a tool call.
func (a *App) requestCtx(ctx context.Context, rs *config.Resolved) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, a.callTimeout(rs))
}

func (a *App) resourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resources <server> [uri]",
		Short: "List a server's resources, or read one",
		Long: `List the resources and resource templates of a server, or read one resource.

  mcpsh resources <server>          resources and templates, one line each
  mcpsh resources <server> <uri>    read a resource: text to stdout, binary data to a file

Fill a template's {placeholders} to get a URI you can read.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := a.resolve(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := a.requestCtx(cmd.Context(), rs)
			defer cancel()
			if len(args) == 2 {
				res, err := a.engine().ReadResource(ctx, rs, args[1])
				if err != nil {
					return err
				}
				if a.jsonOutput() {
					return writeJSON(a.Stdout, res)
				}
				return render.Resource(a.Stdout, res, rs.Name+"-resource")
			}
			list, err := a.engine().ListResources(ctx, rs)
			if err != nil {
				return err
			}
			sort.Slice(list.Resources, func(i, j int) bool { return list.Resources[i].URI < list.Resources[j].URI })
			sort.Slice(list.Templates, func(i, j int) bool { return list.Templates[i].URITemplate < list.Templates[j].URITemplate })
			if a.jsonOutput() {
				if list.Resources == nil {
					list.Resources = []*mcp.Resource{}
				}
				if list.Templates == nil {
					list.Templates = []*mcp.ResourceTemplate{}
				}
				return writeJSON(a.Stdout, list)
			}
			w := a.Stdout
			fmt.Fprintf(w, "Resources of %q (%d):\n", rs.Name, len(list.Resources))
			rows := make([][2]string, 0, len(list.Resources))
			for _, r := range list.Resources {
				rows = append(rows, [2]string{r.URI, describe(r.Title, r.Name, r.Description, r.MIMEType)})
			}
			printRows(w, rows)
			if len(list.Templates) > 0 {
				fmt.Fprintf(w, "\nResource templates (%d):\n", len(list.Templates))
				rows = rows[:0]
				for _, t := range list.Templates {
					rows = append(rows, [2]string{t.URITemplate, describe(t.Title, t.Name, t.Description, t.MIMEType)})
				}
				printRows(w, rows)
			}
			fmt.Fprintf(w, "\nRun \"mcpsh resources %s <uri>\" to read one.\n", rs.Name)
			return nil
		},
	}
}

func (a *App) promptsCmd() *cobra.Command {
	var argList []string
	cmd := &cobra.Command{
		Use:   "prompts <server> [prompt]",
		Short: "List a server's prompts, or get one",
		Long: `List the prompts of a server, or get one rendered with its arguments.

  mcpsh prompts <server>                                prompts and their arguments
  mcpsh prompts <server> <prompt> --arg name=value ...  the prompt's messages

Arguments marked * are required.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := a.resolve(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := a.requestCtx(cmd.Context(), rs)
			defer cancel()
			prompts, err := a.engine().ListPrompts(ctx, rs)
			if err != nil {
				return err
			}
			sort.Slice(prompts, func(i, j int) bool { return prompts[i].Name < prompts[j].Name })
			if len(args) == 1 {
				if len(argList) > 0 {
					return usagef("see mcpsh prompts --help", "--arg needs a prompt name")
				}
				return a.printPrompts(rs, prompts)
			}
			return a.getPrompt(ctx, rs, prompts, args[1], argList)
		},
	}
	cmd.Flags().StringArrayVarP(&argList, "arg", "a", nil, "prompt argument as name=value (repeatable)")
	return cmd
}

func (a *App) printPrompts(rs *config.Resolved, prompts []*mcp.Prompt) error {
	if a.jsonOutput() {
		if prompts == nil {
			prompts = []*mcp.Prompt{}
		}
		return writeJSON(a.Stdout, prompts)
	}
	w := a.Stdout
	fmt.Fprintf(w, "Prompts of %q (%d):\n", rs.Name, len(prompts))
	rows := make([][2]string, 0, len(prompts))
	for _, p := range prompts {
		desc := describe(p.Title, "", p.Description, "")
		if len(p.Arguments) > 0 {
			names := make([]string, len(p.Arguments))
			for i, arg := range p.Arguments {
				names[i] = arg.Name
				if arg.Required {
					names[i] += "*"
				}
			}
			desc = strings.TrimSpace(desc + " (args: " + strings.Join(names, ", ") + ")")
		}
		rows = append(rows, [2]string{p.Name, desc})
	}
	printRows(w, rows)
	fmt.Fprintf(w, "\nRun \"mcpsh prompts %s <prompt> --arg name=value\" to get one.\n", rs.Name)
	return nil
}

func (a *App) getPrompt(ctx context.Context, rs *config.Resolved, prompts []*mcp.Prompt, name string, argList []string) error {
	hint := fmt.Sprintf("run \"mcpsh prompts %s\" to list its prompts and arguments", rs.Name)
	var p *mcp.Prompt
	for _, q := range prompts {
		if q.Name == name {
			p = q
		}
	}
	if p == nil {
		return usagef(hint, fmt.Sprintf("server %q has no prompt %q", rs.Name, name))
	}
	known := map[string]bool{}
	for _, arg := range p.Arguments {
		known[arg.Name] = true
	}
	values := map[string]string{}
	for _, kv := range argList {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return usagef(hint, fmt.Sprintf("--arg %q must be name=value", kv))
		}
		if !known[k] {
			return usagef(hint, fmt.Sprintf("prompt %q has no argument %q", name, k))
		}
		values[k] = v
	}
	var missing []string
	for _, arg := range p.Arguments {
		if _, ok := values[arg.Name]; arg.Required && !ok {
			missing = append(missing, arg.Name)
		}
	}
	if len(missing) > 0 {
		return usagef(hint, fmt.Sprintf("prompt %q needs --arg %s=...", name, strings.Join(missing, "=... --arg ")))
	}
	res, err := a.engine().GetPrompt(ctx, rs, name, values)
	if err != nil {
		return err
	}
	if a.jsonOutput() {
		return writeJSON(a.Stdout, res)
	}
	return render.Prompt(a.Stdout, res, rs.Name+"-"+name)
}

// describe joins what is known about an item into one line.
func describe(title, name, description, mimeType string) string {
	parts := []string{}
	if title != "" {
		parts = append(parts, title)
	} else if name != "" {
		parts = append(parts, name)
	}
	if d := firstLine(description); d != "" {
		if len(parts) > 0 {
			d = "- " + d
		}
		parts = append(parts, d)
	}
	if mimeType != "" {
		parts = append(parts, "["+mimeType+"]")
	}
	return truncate(strings.Join(parts, " "), 100)
}

func printRows(w io.Writer, rows [][2]string) {
	width := 0
	for _, r := range rows {
		width = max(width, len(r[0]))
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  %-*s  %s\n", width, r[0], r[1])
	}
}
