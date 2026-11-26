// Package az implements the subset of the Azure CLI used by T3 Code.
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

func azureOptionToken(arg string) bool {
	if strings.HasPrefix(arg, "--") {
		return true
	}
	key, _, _ := strings.Cut(arg, "=")
	if len(key) != 2 || key[0] != '-' {
		return false
	}
	letter := key[1]
	return (letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z')
}

func parse(args []string) (string, map[string]string, error) {
	i := commandWords(args)
	command := strings.Join(args[:i], " ")
	spec, ok := azureCommands[command]
	if !ok {
		return "", nil, errors.New("unknown command")
	}
	names := strings.Fields(spec.options)
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
		switch azureOptionKind(name) {
		case flagOption:
			if joined {
				return "", nil, fmt.Errorf("--%s takes no value", name)
			}
			v = "true"
		case listOption:
			var values []string
			if joined {
				values = append(values, v)
			}
			for i < len(args) && !strings.HasPrefix(args[i], "-") {
				values = append(values, args[i])
				i++
			}
			if len(values) > maxInvokeParameters || slices.ContainsFunc(values, func(value string) bool {
				return (name != "fields" && len(value) > maxInvokeParameterBytes) || strings.ContainsRune(value, 0)
			}) {
				return "", nil, fmt.Errorf("invalid --%s list", name)
			}
			v = strings.Join(values, "\x00")
		case valueOption:
			if !joined {
				if i == len(args) || azureOptionToken(args[i]) {
					return "", nil, fmt.Errorf("--%s requires a value", name)
				}
				v = args[i]
				i++
			}
		}
		if v == "" && name != "description" {
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
		return "", nil, errors.New("these Azure DevOps commands require JSON output")
	}
	return command, opts, nil
}

type usageError struct{ error }

func (e usageError) Error() string {
	return "unsupported invocation: " + e.error.Error() +
		".\nThis is TFSaz, an Azure CLI compatibility tool for Azure DevOps Server. Run tfsaz --help or see https://github.com/toolspell/tfsaz#readme"
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
	a := app{stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin, store: processStore}
	return a.run(ctx, args)
}

func (a app) run(ctx context.Context, args []string) error {
	if topic, ok := helpTopic(args); ok {
		return writeHelp(a.stdout, topic)
	}
	if len(args) > 0 {
		switch args[0] {
		case "route":
			return a.runRoute(ctx, args[1:])
		case "repo":
			if len(args) >= 2 && args[1] == "ssh" {
				return runSetupSSH(ctx, args[2:])
			}
			return a.runRepo(ctx, args[1:])
		case "init", "profile":
			return a.runProfiles(ctx, args)
		case "doctor":
			return a.runDoctor(ctx, args[1:])
		case "shim":
			return a.runShim(args[1:])
		case "--version":
			if len(args) == 1 {
				_, err := fmt.Fprintln(a.stdout, "tfsaz 0.1.0")
				return err
			}
		}
	}
	command, opts, err := parse(args)
	if err != nil {
		return usageError{err}
	}
	ctx, trace := startCommandTrace(ctx, a.stderr, command)
	defer trace.finish()
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
	case "repos pr create", "repos pr update", "boards work-item create", "boards work-item update":
		err = a.expandWriteText(opts)
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
		trace.selectProfile(*p, traceOrganization)
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
	var p *Profile
	var scope Scope
	selection := traceCheckout
	if command == "repos pr checkout" {
		selection = traceGitRemote
		p, scope, err = cfg.repoCheckoutScope(ctx, a.cwd, opts)
	} else {
		if opts["organization"] != "" {
			selection = traceOrganization
		}
		p, scope, err = cfg.scope(ctx, a.cwd, opts)
	}
	if err != nil {
		return err
	}
	trace.selectProfile(*p, selection)
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
			return client.permissionError(ctx, err)
		}
		return err
	}
	if command == "devops invoke" {
		err = invoke.run(ctx, client, scope, a.cwd, a.stdout)
		if invoke.witResource != "" {
			return client.permissionError(ctx, err)
		}
		return err
	}
	var result any
	switch command {
	case "repos create", "repos pr checkout":
		result, err = a.repoCommand(ctx, client, scope, command, opts)
	case "repos pr create", "repos pr update", "repos pr reviewer add", "repos pr reviewer remove":
		result, err = runPRWrite(ctx, client, scope, command, opts)
	case "boards work-item create", "boards work-item update":
		result, err = a.boardsWrite(ctx, client, scope, command, opts)
	case "boards work-item show":
		result, err = item.run(ctx, client, scope)
		err = client.permissionError(ctx, err)
	case "devops team list", "devops team list-member":
		result, err = teams.run(ctx, client, scope)
		err = client.permissionError(ctx, err)
	default:
		result, err = runRepos(ctx, client, scope, command, opts)
	}
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	return json.NewEncoder(a.stdout).Encode(result)
}

type missingPATError struct{ profile Profile }

func (e missingPATError) Error() string {
	return fmt.Sprintf("profile %s has no PAT; use az devops login --organization %s", e.profile.ID, e.profile.Organization)
}

func (a app) client(p Profile) (*Client, error) {
	pat, err := a.profileCredential(p)
	if errors.Is(err, errNoCredential) || (err == nil && pat == "") {
		return nil, missingPATError{p}
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
	trace := getCommandTrace(ctx)
	for i, p := range cfg.Profiles {
		trace.selectProfile(p, traceDiscovery)
		client, err := a.client(p)
		if err == nil {
			probe, end := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(cfg.Profiles)-i))
			id, probeErr := client.Identity(probe)
			end()
			if probeErr == nil {
				for _, failure := range failures {
					if errors.Is(failure, errStoreUnavailable) {
						// A warning only: the login found here is the result.
						_, _ = fmt.Fprintln(a.stderr, failure)
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
