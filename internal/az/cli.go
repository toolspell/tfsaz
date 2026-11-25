// Package az implements the subset of the Azure CLI used by T3's read-only flows.
package az

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

const usage = `tfsaz — Azure DevOps Server for T3 Code

  tfsaz --version
  tfsaz repo setup [PATH] [--profile ID] [--ssh-url URL]
  tfsaz repo verify [PATH] | repo list | repo restore [PATH]
  tfsaz devops login --organization URL
  az account show --query user.name -o tsv
  az account show --query user --output json
  az repos show | repos pr list | repos pr show | repos pr policy list
  az boards work-item show --id ID [--expand relations]
  az devops team list | devops team list-member
  az devops invoke [--area AREA [--resource RESOURCE]] [--route-parameters key=value ...]
  az rest --url COLLECTION_API_URL [--method METHOD]

Configure collection profiles in tfsaz/config.json in your OS user configuration
directory, or select an absolute config file with TFSAZ_CONFIG. Login reads a PAT
from a hidden prompt or stdin and stores it in the OS keyring.`

// These are the option sets emitted by the adapter, including its joined
// --repository=-name and --project=-name forms.
var commandOptions = map[string]string{
	"devops login":            "organization",
	"devops invoke":           "organization detect only-show-errors output area resource api-version route-parameters query-parameters http-method in-file media-type accept-media-type out-file query encoding",
	"rest":                    "url method body headers uri-parameters output-file output query only-show-errors",
	"devops team list":        "organization project detect only-show-errors output top skip",
	"devops team list-member": "organization project detect only-show-errors output team top skip",
	"boards work-item show":   "organization id detect only-show-errors output expand",
	"account show":            "query output organization only-show-errors",
	"repos show":              "organization project repository detect only-show-errors output",
	"repos pr show":           "organization id detect only-show-errors output",
	"repos pr list":           "organization project repository detect only-show-errors output top skip source-branch target-branch status include-links creator reviewer",
	"repos pr policy list":    "organization id detect only-show-errors output top skip",
}
var optionNames = map[string]string{"--uri": "url", "-u": "url", "-m": "method", "-b": "body", "--url-parameters": "uri-parameters", "--org": "organization", "-o": "output", "-p": "project", "-r": "repository", "-i": "id"}

func parse(args []string) (string, map[string]string, error) {
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") {
		i++
	}
	command := strings.Join(args[:i], " ")
	allowed, ok := commandOptions[command]
	if !ok {
		return "", nil, errors.New("unknown command")
	}
	names := strings.Fields(allowed)
	opts := map[string]string{}
	for i < len(args) {
		key, v, joined := strings.Cut(args[i], "=")
		i++
		name, alias := optionNames[key]
		if !alias {
			name, ok = strings.CutPrefix(key, "--")
			if !ok {
				return "", nil, errors.New("unsupported option or unexpected argument for " + command)
			}
		}
		if !slices.Contains(names, name) {
			return "", nil, errors.New("unsupported option for " + command)
		}
		if _, exists := opts[name]; exists {
			return "", nil, fmt.Errorf("duplicate --%s", name)
		}
		if name == "include-links" || name == "only-show-errors" {
			if joined {
				return "", nil, fmt.Errorf("--%s takes no value", name)
			}
			v = "true"
		} else if name == "route-parameters" || name == "query-parameters" || name == "uri-parameters" || name == "headers" {
			var values []string
			if joined {
				values = append(values, v)
			}
			for i < len(args) && !strings.HasPrefix(args[i], "-") {
				values = append(values, args[i])
				i++
			}
			if len(values) > maxInvokeParameters || slices.ContainsFunc(values, func(value string) bool {
				return len(value) > maxInvokeParameterBytes || strings.ContainsRune(value, 0)
			}) {
				return "", nil, fmt.Errorf("invalid --%s list", name)
			}
			v = strings.Join(values, "\x00")
		} else if !joined {
			if i == len(args) || strings.HasPrefix(args[i], "--") {
				return "", nil, fmt.Errorf("--%s requires a value", name)
			}
			v = args[i]
			i++
		}
		if v == "" {
			return "", nil, fmt.Errorf("--%s requires a value", name)
		}
		opts[name] = v
	}
	if d := opts["detect"]; d != "" && d != "true" && d != "false" {
		return "", nil, errors.New("--detect expects true or false")
	}
	switch opts["status"] {
	case "", "active", "abandoned", "completed", "all":
	default:
		return "", nil, errors.New("--status expects active, abandoned, completed, or all")
	}
	output := opts["output"]
	if command == "account show" {
		viewer := opts["query"] == "user" && (output == "" || output == "json")
		discovery := opts["query"] == "user.name" && output == "tsv"
		if !viewer && !discovery {
			return "", nil, errors.New("supported account queries are --query user -o json and --query user.name -o tsv")
		}
	} else if command != "devops invoke" && command != "rest" && output != "" && output != "json" {
		return "", nil, errors.New("Azure DevOps reads require JSON output")
	}
	return command, opts, nil
}

type usageError struct{ error }

func (e usageError) Error() string {
	return "unsupported invocation: " + e.error.Error() +
		".\nThis is tfsaz, an Azure CLI compatibility tool for Azure DevOps Server. Run tfsaz --help or see https://github.com/toolspell/tfsaz#readme"
}

func (e usageError) Unwrap() error { return e.error }

// ExitCode maps command errors to the executable's exit status.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

type app struct {
	cwd            string
	stdout, stderr io.Writer
	stdin          *os.File
	store          credentialStore
}

// Run writes command output to stdout. The executable reports returned errors
// on stderr and exits nonzero.
func Run(ctx context.Context, args []string) error {
	a := app{stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin, store: systemStore{}}
	return a.run(ctx, args)
}

func (a app) run(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "repo" {
		if len(args) >= 2 && args[1] == "ssh" {
			return runSetupSSH(ctx, args[2:])
		}
		return a.runRepo(ctx, args[1:])
	}
	if len(args) == 1 {
		switch args[0] {
		case "--version":
			_, err := fmt.Fprintln(a.stdout, "tfsaz 0.1.0")
			return err
		case "--help", "-h":
			_, err := fmt.Fprintln(a.stdout, usage)
			return err
		}
	}
	command, opts, err := parse(args)
	if err != nil {
		return usageError{err}
	}
	var invoke invokeRead
	var generic *apiCall
	var item workItemRead
	var teams teamRead
	switch command {
	case "devops invoke":
		// Preserve the adapter's URL normalization, file handling and workflow
		// projection for the precise request shapes it already uses.
		if stockOutput(opts) {
			invoke, err = parseStockInvoke(opts, a.cwd)
			if err == nil {
				break
			}
		}
		fallthrough
	case "rest":
		generic, err = parseAPICall(command, opts, a.cwd)
	case "boards work-item show":
		item, err = parseWorkItemShow(opts)
	case "devops team list", "devops team list-member":
		teams, err = parseTeamRead(command, opts)
	}
	if err != nil {
		return usageError{err}
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	records, err := readSetupRecords()
	if err != nil {
		return err
	}
	if err := cfg.addSetupAliases(records); err != nil {
		return err
	}
	if command == "devops login" {
		p, _ := cfg.selectBase(opts["organization"])
		if p == nil {
			return errors.New("login requires --organization with a configured collection")
		}
		return a.login(ctx, *p)
	}
	if command == "account show" && opts["query"] == "user.name" && opts["organization"] == "" {
		login, err := a.discover(ctx, cfg)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.stdout, login)
		return err
	}
	// Stock T3 allows 30 seconds per CLI read. Direct REST calls can also
	// transfer a file and need a longer budget.
	timeout := 27 * time.Second
	if generic != nil {
		timeout = genericRESTTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if command == "rest" {
		return a.runRest(ctx, cfg, generic, opts["url"])
	}
	p, scope, err := cfg.scope(ctx, a.cwd, opts)
	if err != nil {
		return err
	}
	client, err := a.client(*p)
	if err != nil {
		return err
	}
	client.checkBoundary = func(path []string) error { return cfg.checkRestBoundary(p, path) }
	if command == "account show" {
		id, err := client.Identity(ctx)
		if err != nil {
			return err
		}
		if opts["query"] == "user.name" {
			_, err = fmt.Fprintln(a.stdout, id.Login)
			return err
		}
		return json.NewEncoder(a.stdout).Encode(map[string]string{"name": id.Login, "type": "user"})
	}
	if generic != nil {
		err := generic.invoke(ctx, client, scope, a.stdout)
		if strings.EqualFold(generic.area, "wit") {
			return client.boardsReadError(ctx, err)
		}
		return err
	}
	if command == "devops invoke" {
		err = invoke.run(ctx, client, scope, a.cwd, a.stdout)
		if invoke.witResource != "" {
			return client.boardsReadError(ctx, err)
		}
		return err
	}
	var result any
	switch command {
	case "boards work-item show":
		result, err = item.run(ctx, client, scope)
		err = client.boardsReadError(ctx, err)
	case "devops team list", "devops team list-member":
		result, err = teams.run(ctx, client, scope)
		err = client.boardsReadError(ctx, err)
	default:
		result, err = runRepos(ctx, client, scope, command, opts)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(a.stdout).Encode(result)
}

func (a app) client(p Profile) (*Client, error) {
	pat, err := a.store.Get(p.ID)
	if errors.Is(err, errNoCredential) || (err == nil && pat == "") {
		return nil, fmt.Errorf("profile %s has no PAT; use az devops login --organization %s", p.ID, p.Organization)
	}
	if err != nil {
		return nil, fmt.Errorf("%w for profile %s", errStoreUnavailable, p.ID)
	}
	return newClient(p, pat)
}

func (a app) discover(ctx context.Context, cfg config) (string, error) {
	// T3 gives the whole discovery command 20 seconds. Leave each remaining
	// collection a chance to answer when an earlier server is offline.
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var failures []error
	for i, p := range cfg.Profiles {
		client, err := a.client(p)
		if err == nil {
			probe, end := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(cfg.Profiles)-i))
			id, probeErr := client.Identity(probe)
			end()
			if probeErr == nil {
				for _, failure := range failures {
					if errors.Is(failure, errStoreUnavailable) {
						fmt.Fprintln(a.stderr, failure)
					}
				}
				return id.Login, nil
			}
			err = probeErr
		}
		failures = append(failures, fmt.Errorf("%s: %w", p.ID, err))
	}
	return "", errors.Join(failures...)
}
