// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"gitea.dev/modules/json"
)

// Command is one documented endpoint. The set lives in generated_client.go, rendered from
// the API's operation set (K2, K7).
type Command struct {
	Name        string
	OperationID string
	Method      string
	Path        string
	Summary     string
	PathParams  []string
	QueryParams []string
	Columns     []string
	IsList      bool
}

// Error carries a message, an exit code and a suggested next action. An error that states
// only what went wrong is incomplete (A21).
type Error struct {
	Message         string
	SuggestedAction string
	ExitCode        int
}

func failf(code int, action, format string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, args...), SuggestedAction: action, ExitCode: code}
}

// Doer is the transport. Tests inject a recorded API here, so no test needs a live
// server (J13).
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
	fs.Var(&filters, "filter", "repeatable `field[op]=value` filter; sent to the server verbatim (I3, K4)")
	q := fs.String("q", "", "free-text search (I10)")
	sortBy := fs.String("sort-by", "", "sort field (I5)")
	order := fs.String("order", "", "asc or desc (I5)")
	limit := fs.Int("limit", 0, "page size; the server defaults to 50 and caps at 200 (I7)")
	offset := fs.Int("offset", 0, "row offset, converted to the 1-based page the API takes (I7)")
	cursor := fs.String("cursor", "", "opaque cursor from a previous response (I6)")
	expand := fs.String("expand", "", "comma-separated sub-resources, one level deep (I9)")
	asJSON := fs.Bool("json", false, "emit the API response verbatim and unshaped (K5)")
	server := fs.String("server", "", "Gitea base URL; defaults to $GITEA_DELIVERY_SERVER or $GITEA_SERVER")
	token := fs.String("token", "", "API token; resolved by the same precedence detect.sh implements (K8)")

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
	// stdout and exits 0. The generated command reference is this output (K11).
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
				"--offset %d is not a multiple of the page size %d, and the API pages by 1-based page (I7)", *offset, size)
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

	req, reqErr := http.NewRequest(cmd.Method, target, nil)
	if reqErr != nil {
		return failf(1, "Check --server is a URL, for example https://gitea.example.com.", "%v", reqErr)
	}
	req.Header.Set("Accept", "application/json")
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
		// Verbatim and unshaped, so no script has to parse a table (K5).
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
