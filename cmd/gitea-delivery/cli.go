// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"gitea.dev/modules/json"
)

// Command is one documented endpoint. The set lives in generated_client.go, rendered from
// the API's operation set.
type Command struct {
	Name        string
	OperationID string
	Method      string
	Path        string
	Summary     string
	PathParams  []string
	QueryParams []string
	// BodyParams are the request body's members, one flag each. RequiredBody is the subset
	// the endpoint refuses the request without, and BoolBody the subset that marshals as a
	// JSON boolean rather than a string.
	BodyParams   []string
	RequiredBody []string
	BoolBody     []string
	// BodyHelp is each member's published description, used as its flag's help text so the
	// generated command reference explains the body rather than listing it.
	BodyHelp map[string]string
	Columns  []string
	IsList   bool
}

// Error carries a message, an exit code and a suggested next action. An error that states
// only what went wrong is incomplete.
type Error struct {
	Message         string
	SuggestedAction string
	ExitCode        int
}

func failf(code int, action, format string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, args...), SuggestedAction: action, ExitCode: code}
}

// Doer is the transport. Tests inject a recorded API here, so no test needs a live
// server.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Transport is swapped by the tests.
var Transport Doer = http.DefaultClient

type filterFlag []string

func (f *filterFlag) String() string     { return strings.Join(*f, ",") }
func (f *filterFlag) Set(v string) error { *f = append(*f, v); return nil }

func run(args []string, stdout, stderr io.Writer) *Error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		usage(stdout)
		return nil
	}
	if args[0] == "--list-operations" {
		for _, c := range sortedCommands() {
			fmt.Fprintf(stdout, "%s\t%s\n", c.OperationID, c.Name)
		}
		return nil
	}

	cmd, ok := lookup(args[0])
	if !ok {
		return failf(2, "Run `gitea-delivery --help` to see every command this build serves.",
			"unknown command %q", args[0])
	}
	return runCommand(cmd, args[1:], stdout, stderr)
}

func runCommand(cmd Command, args []string, stdout, stderr io.Writer) *Error {
	fs := flag.NewFlagSet("gitea-delivery "+cmd.Name, flag.ContinueOnError)
	fs.SetOutput(stderr)

	var filters filterFlag
	fs.Var(&filters, "filter", "repeatable `field[op]=value` filter; sent to the server verbatim")
	q := fs.String("q", "", "free-text search")
	sortBy := fs.String("sort-by", "", "sort field")
	order := fs.String("order", "", "asc or desc")
	limit := fs.Int("limit", 0, "page size; the server defaults to 50 and caps at 200")
	offset := fs.Int("offset", 0, "row offset, converted to the 1-based page the API takes")
	cursor := fs.String("cursor", "", "opaque cursor from a previous response")
	expand := fs.String("expand", "", "comma-separated sub-resources, one level deep")
	asJSON := fs.Bool("json", false, "emit the API response verbatim and unshaped")
	server := fs.String("server", "", "Gitea base URL; defaults to $GITEA_DELIVERY_SERVER or $GITEA_SERVER")
	token := fs.String("token", "", "API token; resolved by the same precedence detect.sh implements")

	// One flag per request-body member, from the published document. deploy and rollback are
	// the same operation, so they take the same flags and compose the identical request,
	// differing only in --release-tag.
	bodyStrings := map[string]*string{}
	bodyBools := map[string]*bool{}
	for _, name := range cmd.BodyParams {
		flagName := strings.ReplaceAll(name, "_", "-")
		help := cmd.BodyHelp[name]
		if help == "" {
			help = "request body: " + name
		}
		if slices.Contains(cmd.RequiredBody, name) {
			help = "required. " + help
		}
		if slices.Contains(cmd.BoolBody, name) {
			bodyBools[name] = fs.Bool(flagName, false, help)
			continue
		}
		bodyStrings[name] = fs.String(flagName, "", help)
	}

	printUsage := func(w io.Writer) {
		fs.SetOutput(w)
		fmt.Fprintf(w, "gitea-delivery %s — %s\n\n", cmd.Name, cmd.Summary)
		fmt.Fprintf(w, "  %s %s\n\n", cmd.Method, cmd.Path)
		if len(cmd.PathParams) > 0 {
			fmt.Fprintf(w, "Positional arguments: %s\n\n", strings.Join(cmd.PathParams, " "))
		}
		fmt.Fprintln(w, "Flags:")
		fs.PrintDefaults()
		fs.SetOutput(stderr)
	}
	fs.Usage = func() { printUsage(stderr) }

	// --help is a successful request for documentation, not a usage error, so it prints on
	// stdout and exits 0. The generated command reference is this output.
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			printUsage(stdout)
			return nil
		}
	}
	if err := fs.Parse(args); err != nil {
		return failf(2, "Run `gitea-delivery "+cmd.Name+" --help` for the accepted flags.", "%v", err)
	}

	positional := fs.Args()
	if len(positional) != len(cmd.PathParams) {
		return failf(2,
			fmt.Sprintf("Call it as `gitea-delivery %s %s`.", cmd.Name, strings.Join(cmd.PathParams, " ")),
			"%s takes %d positional argument(s), got %d", cmd.Name, len(cmd.PathParams), len(positional))
	}

	baseURL, err := resolveServer(*server)
	if err != nil {
		return err
	}
	authToken, err := resolveToken(*token, baseURL)
	if err != nil {
		return err
	}

	values := url.Values{}
	for _, f := range filters {
		name, value, found := strings.Cut(f, "=")
		if !found {
			return failf(2, "Write the filter as `field[op]=value`, for example --filter 'created_at[gte]=2026-01-01T00:00:00Z'.",
				"filter %q has no '='", f)
		}
		values.Add(name, value)
	}
	setIf(values, "q", *q)
	setIf(values, "sort_by", *sortBy)
	setIf(values, "order", *order)
	setIf(values, "expand", *expand)
	setIf(values, "cursor", *cursor)
	if *limit > 0 {
		values.Set("limit", strconv.Itoa(*limit))
	}
	if *offset > 0 {
		size := *limit
		if size <= 0 {
			size = 50
		}
		if *offset%size != 0 {
			return failf(2, fmt.Sprintf("Use an --offset that is a multiple of --limit (%d), or page with --limit and no --offset.", size),
				"--offset %d is not a multiple of the page size %d, and the API pages by 1-based page", *offset, size)
		}
		values.Set("page", strconv.Itoa(*offset/size+1))
	}

	path := cmd.Path
	for i, name := range cmd.PathParams {
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(positional[i]))
	}
	target := baseURL + "/api/delivery/v1" + path
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}

	payload, bodyErr := composeBody(cmd, bodyStrings, bodyBools)
	if bodyErr != nil {
		return bodyErr
	}

	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}
	req, reqErr := http.NewRequest(cmd.Method, target, bodyReader)
	if reqErr != nil {
		return failf(1, "Check --server is a URL, for example https://gitea.example.com.", "%v", reqErr)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authToken != "" {
		req.Header.Set("Authorization", "token "+authToken)
	}

	resp, doErr := Transport.Do(req)
	if doErr != nil {
		return failf(1, "Check the server is reachable and that --server names it: "+baseURL, "%v", doErr)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return failf(1, "Retry the request; the response was cut short.", "%v", readErr)
	}

	if resp.StatusCode >= 400 {
		return apiFailure(resp.StatusCode, body)
	}
	if *asJSON {
		// Verbatim and unshaped, so no script has to parse a table.
		if _, err := stdout.Write(body); err != nil {
			return failf(1, "Check the destination of stdout.", "%v", err)
		}
		if len(body) > 0 && body[len(body)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
		return nil
	}
	return renderTable(cmd, body, stdout)
}

// composeBody builds the JSON request body from the body flags. It returns nil for a command
// that takes none, so a GET is sent with no body exactly as before.
//
// The bytes are deterministic: members are written in the order the generated command lists
// them, which is sorted. That is what makes "deploy and rollback compose the identical
// request" a byte comparison rather than a structural one.
func composeBody(cmd Command, stringValues map[string]*string, bools map[string]*bool) ([]byte, *Error) {
	if len(cmd.BodyParams) == 0 {
		return nil, nil
	}
	var missing []string
	for _, name := range cmd.RequiredBody {
		if v, ok := stringValues[name]; ok && strings.TrimSpace(*v) == "" {
			missing = append(missing, "--"+strings.ReplaceAll(name, "_", "-"))
		}
	}
	if len(missing) > 0 {
		return nil, failf(2,
			fmt.Sprintf("Call it as `gitea-delivery %s %s`.", cmd.Name, strings.Join(missing, " <value> ")+" <value>"),
			"%s needs %s", cmd.Name, strings.Join(missing, ", "))
	}

	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	for _, name := range cmd.BodyParams {
		var encoded []byte
		switch {
		case bools[name] != nil:
			if !*bools[name] {
				continue // an unset switch is omitted, so the server's own default stands
			}
			encoded = []byte("true")
		case stringValues[name] != nil:
			if *stringValues[name] == "" {
				continue
			}
			raw, err := json.Marshal(*stringValues[name])
			if err != nil {
				return nil, failf(1, "Remove any unprintable characters from the value.", "%v", err)
			}
			encoded = raw
		default:
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		key, err := json.Marshal(name)
		if err != nil {
			return nil, failf(1, "Regenerate the client with `make delivery-generate`.", "%v", err)
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(encoded)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func apiFailure(status int, body []byte) *Error {
	var payload struct {
		Message         string   `json:"message"`
		SuggestedAction string   `json:"suggested_action"`
		Accepted        []string `json:"accepted"`
	}
	action := "Run `gitea-delivery <command> --help`, and check your token has access."
	message := fmt.Sprintf("the API returned %d: %s", status, strings.TrimSpace(string(body)))
	if err := json.Unmarshal(body, &payload); err == nil && payload.Message != "" {
		message = payload.Message
		if payload.SuggestedAction != "" {
			action = payload.SuggestedAction
		}
		if len(payload.Accepted) > 0 {
			action += " Accepted: " + strings.Join(payload.Accepted, ", ") + "."
		}
	}
	code := 1
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		code = 3
	}
	return &Error{Message: message, SuggestedAction: action, ExitCode: code}
}

func renderTable(cmd Command, body []byte, stdout io.Writer) *Error {
	var rows []map[string]any
	if cmd.IsList {
		if err := json.Unmarshal(body, &rows); err != nil {
			return failf(1, "Re-run with --json to see what the API returned.", "response is not a JSON array: %v", err)
		}
	} else {
		var single map[string]any
		if err := json.Unmarshal(body, &single); err != nil {
			return failf(1, "Re-run with --json to see what the API returned.", "response is not a JSON object: %v", err)
		}
		rows = []map[string]any{single}
	}

	columns := cmd.Columns
	if len(columns) == 0 && len(rows) > 0 {
		for k := range rows[0] {
			columns = append(columns, k)
		}
		sort.Strings(columns)
	}

	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(upperAll(columns), "\t"))
	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, col := range columns {
			cells[i] = cell(row[col])
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	if err := w.Flush(); err != nil {
		return failf(1, "Check the destination of stdout.", "%v", err)
	}
	return nil
}

func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

func upperAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.ToUpper(v)
	}
	return out
}

func setIf(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}

func lookup(name string) (Command, bool) {
	for _, c := range Commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

func sortedCommands() []Command {
	out := append([]Command(nil), Commands...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func usage(stdout io.Writer) {
	fmt.Fprintln(stdout, "gitea-delivery — a thin client over /api/delivery/v1.")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Usage: gitea-delivery <command> [positional...] [flags]")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Commands:")
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, c := range sortedCommands() {
		fmt.Fprintf(w, "  %s\t%s\n", c.Name, c.Summary)
	}
	w.Flush()
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Every command accepts the section I query grammar as flags:")
	fmt.Fprintln(stdout, "  --filter 'field[op]=value'  repeatable; sent to the server, never applied locally")
	fmt.Fprintln(stdout, "  -q, --sort-by, --order      search and sort")
	fmt.Fprintln(stdout, "  --limit, --offset, --cursor pagination")
	fmt.Fprintln(stdout, "  --expand                    sub-resources, one level deep")
	fmt.Fprintln(stdout, "  --json                      the API response verbatim and unshaped")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Credentials resolve as: --token, then $GITEA_DELIVERY_TOKEN, then $FORGE_TOKEN, then $GITEA_TOKEN.")
	fmt.Fprintln(stdout, "Exit codes: 0 success, 1 request failed, 2 usage error, 3 refused by the server.")
}
