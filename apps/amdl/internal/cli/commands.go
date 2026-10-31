package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"amdl/apps/amdl/internal/jobview"
	"amdl/internal/capability"
)

// custom returns the commands written out by hand; every other capability with
// an operation becomes a passthrough command built from the spec.
func custom() map[string]func(fs *flag.FlagSet) func(e *env, args []string) error {
	return map[string]func(fs *flag.FlagSet) func(e *env, args []string) error{
		"download":          downloadCmd,
		"jobs get":          jobsGetCmd,
		"jobs watch":        jobsWatchCmd,
		"jobs wait":         jobsWaitCmd,
		"library list":      libraryListCmd,
		"logs tail":         logsTailCmd,
		"config get":        configGetCmd,
		"config set":        configSetCmd,
		"wrapper login":     wrapperLoginCmd,
		"wrapper 2fa":       wrapperTwoStepCmd,
		"cli search":        cliSearchCmd,
		"cli commands":      cliCommandsCmd,
		"cli setup":         cliSetupCmd,
		"schema":            schemaCmd,
		"context list":      noFlags(contextList),
		"context add":       noFlags(contextAdd),
		"context use":       noFlags(contextUse),
		"context remove":    noFlags(contextRemove),
		"version":           versionCmd,
		"serve":             serveCmd,
		"mcp":               mcpCmd,
		"tui":               tuiCmd,
		"doctor":            doctorCmd,
		"wrapper install":   wrapperInstallCmd(false),
		"wrapper update":    wrapperInstallCmd(true),
		"wrapper import":    wrapperImportCmd,
		"wrapper uninstall": wrapperUninstallCmd,
		"wrapper start":     wrapperSwitchCmd(true),
		"wrapper stop":      wrapperSwitchCmd(false),
		"wrapper logs":      wrapperLogsCmd,
		"service install":   serviceInstallCmd,
		"service uninstall": serviceUninstallCmd,
		"service start":     serviceStartCmd,
		"service stop":      serviceStopCmd,
		"service status":    serviceStatusCmd,
	}
}

// offline are the commands that never talk to a server.
var offline = map[string]bool{
	"cli search": true, "cli commands": true, "cli setup": true, "schema": true, "context list": true,
	"context add": true, "context use": true, "context remove": true, "serve": true, "version": true,
	"doctor": true, "wrapper install": true, "wrapper update": true, "wrapper import": true, "wrapper uninstall": true,
	"wrapper start": true, "wrapper stop": true, "wrapper logs": true, "service install": true, "service uninstall": true, "service start": true, "service stop": true, "service status": true,
}

// planned are the offline commands that still take --dry-run: they print what
// they would register or remove instead of sending a request.
var planned = map[string]bool{"service install": true, "service uninstall": true}

func noFlags(run func(e *env, args []string) error) func(*flag.FlagSet) func(*env, []string) error {
	return func(*flag.FlagSet) func(*env, []string) error { return run }
}

// commands is every CLI command, in capability order.
func commands() []command {
	var out []command
	handwritten := custom()
	for _, c := range capability.All {
		if c.Command == "" {
			continue
		}
		cmd := command{Capability: c, noServer: offline[c.Command], serverOptional: c.Command == "mcp"}
		if setup, ok := handwritten[c.Command]; ok {
			cmd.setup = setup
			cmd.dryRun = c.Safety != capability.Read && !cmd.serverOptional && (!cmd.noServer || planned[c.Command])
		} else {
			cmd.setup = passthrough(c)
			cmd.dryRun = c.Safety != capability.Read
		}
		out = append(out, cmd)
	}
	return out
}

// flagName is the CLI spelling of a parameter or body field.
func flagName(param string) string { return strings.ReplaceAll(param, "_", "-") }

// specFlags declares one flag per parameter and body field of op that is not
// positional, typed from its schema, and returns the values they collect.
func specFlags(fs *flag.FlagSet, op capability.Operation, positional []string) map[string]any {
	values := map[string]any{}
	skip := map[string]bool{}
	for _, a := range positional {
		skip[a] = true
	}
	add := func(name, typ, desc string, enum []any) {
		if skip[name] || fs.Lookup(flagName(name)) != nil {
			return
		}
		usage := firstSentence(desc)
		if len(enum) > 0 {
			usage += fmt.Sprintf(" (%s)", joinAny(enum))
		}
		switch typ {
		case "boolean":
			fs.BoolFunc(flagName(name), usage, func(s string) error {
				b, err := strconv.ParseBool(s)
				values[name] = b
				return err
			})
		case "integer":
			fs.Func(flagName(name), usage, func(s string) error {
				n, err := strconv.Atoi(s)
				if err != nil {
					return fmt.Errorf("not a whole number: %s", s)
				}
				values[name] = n
				return nil
			})
		case "array":
			fs.Func(flagName(name), usage+"; comma-separated", func(s string) error {
				values[name] = append(asStrings(values[name]), splitList(s)...)
				return nil
			})
		case "object":
			fs.Func(flagName(name), usage+"; a JSON object", func(s string) error {
				var obj map[string]any
				if err := json.Unmarshal([]byte(s), &obj); err != nil {
					return fmt.Errorf("not a JSON object: %s", s)
				}
				values[name] = obj
				return nil
			})
		default:
			fs.Func(flagName(name), usage, func(s string) error {
				values[name] = s
				return nil
			})
		}
	}
	for _, p := range op.Params {
		var enum []any
		if items, ok := p.Schema["items"].(map[string]any); ok {
			enum, _ = items["enum"].([]any)
		} else {
			enum, _ = p.Schema["enum"].([]any)
		}
		add(p.Name, p.Type(), p.Description, enum)
	}
	for _, f := range op.BodyFields() {
		typ, _ := f.Schema["type"].(string)
		enum, _ := f.Schema["enum"].([]any)
		add(f.Name, typ, f.Description, enum)
	}
	return values
}

// paletteCommands answer with catalog hits that carry the artwork color
// palette, which an agent has no use for; they leave it out unless
// --artwork-colors asks for it, as the MCP tools do.
var paletteCommands = map[string]bool{"search": true, "lookup": true, "artists albums": true, "albums get": true}

// passthrough builds a command that sends one request from its flags and
// positional arguments and prints the answer as is.
func passthrough(c capability.Capability) func(fs *flag.FlagSet) func(e *env, args []string) error {
	return func(fs *flag.FlagSet) func(e *env, args []string) error {
		op := capability.Lookup(c.Ops[0])
		values := specFlags(fs, op, c.Args)
		var palette *bool
		if paletteCommands[c.Command] {
			palette = fs.Bool("artwork-colors", false, "keep the artwork color palette (artwork_bg_color, artwork_text_color1 to 4) in the hits")
		}
		return func(e *env, args []string) error {
			if len(args) > len(c.Args) {
				return usageErr("too many arguments; usage: amdl %s%s", c.Command, usageLine(c, op))
			}
			for i, a := range args {
				values[c.Args[i]] = a
			}
			req, err := op.Build(values)
			if err != nil {
				return usageErr("%v; usage: amdl %s%s", err, c.Command, usageLine(c, op))
			}
			if palette != nil && !*palette {
				return e.send(req, func(raw []byte) error { return e.out.result(jobview.DropPalette(raw)) })
			}
			return e.send(req, nil)
		}
	}
}

// usageLine is the command's synopsis followed by the flags its request
// requires, such as --username <username> for wrapper logout.
func usageLine(c capability.Capability, op capability.Operation) string {
	line := argSynopsis(c)
	positional := map[string]bool{}
	for _, a := range c.Args {
		positional[a] = true
	}
	for _, p := range op.Params {
		if p.Required && !positional[p.Name] {
			line += " --" + flagName(p.Name) + " <" + p.Name + ">"
		}
	}
	for _, f := range op.BodyFields() {
		if f.Required && !positional[f.Name] {
			line += " --" + flagName(f.Name) + " <" + f.Name + ">"
		}
	}
	return line
}

// send runs one built request, honouring --dry-run and confirmation, and
// prints the answer, or out when it is not nil.
func (e *env) send(req capability.Request, out func([]byte) error) error {
	if e.dry {
		return e.out.result(map[string]any{"method": req.Method, "url": e.c.Base() + req.Path + query(req), "body": req.Body})
	}
	if e.cmd.Safety == capability.Destructive {
		if err := e.confirm(fmt.Sprintf("amdl %s changes data that cannot be restored", e.cmd.Command)); err != nil {
			return err
		}
	}
	var body any
	if req.Body != nil {
		body = req.Body
	}
	raw, err := e.c.Call(e.ctx, req.Method, req.Path, req.Query, body)
	if err != nil {
		return err
	}
	if out != nil {
		return out(raw)
	}
	return e.out.result(raw)
}

func query(req capability.Request) string {
	if len(req.Query) == 0 {
		return ""
	}
	return "?" + req.Query.Encode()
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, ".")
}

func joinAny(list []any) string {
	parts := make([]string, len(list))
	for i, v := range list {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, "|")
}

func asStrings(v any) []string {
	s, _ := v.([]string)
	return s
}
