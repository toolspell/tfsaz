package az

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

type azureCommand struct {
	description string
	options     string
	example     string
	notes       string
}

// The Azure-compatible parser and offline help use the same option sets.
var azureCommands = map[string]azureCommand{
	"account show": {
		"Read the authenticated user.",
		"query output organization only-show-errors",
		"account show --query user.name -o tsv",
		"Use --query user for the JSON identity of the selected collection, or --query user.name -o tsv for a login on one line. Without --organization the JSON query uses the checkout and the TSV query checks configured profiles in order.",
	},
	"repos show": {
		"Read an Azure DevOps Git repository.",
		"organization project repository detect only-show-errors output",
		"repos show --project Project --repository Repo",
		"Requires a project and repository. Both can come from the checkout.",
	},
	"repos create": {
		"Create a Git repository in an Azure DevOps project.",
		"organization project detect name only-show-errors output",
		"repos create --project Project --name Repo",
		"Requires --name and a project. The project can come from the checkout.",
	},
	"repos pr list": {
		"List pull requests for a project or repository.",
		"organization project repository detect only-show-errors output top skip source-branch target-branch status include-links creator reviewer",
		"repos pr list --status active --reviewer me --include-links --top 20",
		"Requires a project. For a project-wide list, supply --organization and --project with --detect false. Use --include-links for PR web links. --creator and --reviewer accept me, a login or an identity GUID.",
	},
	"repos pr show": {
		"Read a pull request by ID.",
		"organization id detect only-show-errors output",
		"repos pr show --id 42",
		"Requires --id. Resolves the ID within the selected collection.",
	},
	"repos pr policy list": {
		"Read one page of policy evaluations for a pull request.",
		"organization id detect only-show-errors output top skip",
		"repos pr policy list --id 42 --top 100 --skip 0",
		"Requires --id. Use --top and --skip to read successive pages.",
	},
	"repos pr checkout": {
		"Fetch and switch to a pull request branch in the current repository.",
		"organization id remote-name detect only-show-errors output",
		"repos pr checkout --id 42 --remote-name origin",
		"Requires --id and a local checkout. The Git remote defaults to origin and must belong to the PR's repository. --organization must match that remote's collection if supplied.",
	},
	"repos pr create": {
		"Create a pull request.",
		"organization project repository detect only-show-errors output source-branch target-branch title description draft squash auto-complete delete-source-branch merge-commit-message transition-work-items",
		"repos pr create --source-branch feature --target-branch main --title \"Add feature\" --description=@notes.md --draft true",
		"Requires both branches, a project and a repository. The checkout can supply the project and repository. Text options accept @FILE or @- for stdin.",
	},
	"repos pr update": {
		"Update a pull request by ID.",
		"organization id detect only-show-errors output title description status squash auto-complete draft delete-source-branch merge-commit-message transition-work-items",
		"repos pr update --id 42 --title \"Updated title\"",
		"Requires --id. Use --status completed to complete a pull request and --squash true to select a squash merge. Text options accept @FILE or @- for stdin.",
	},
	"repos pr reviewer add": {
		"Add reviewers to a pull request.",
		"organization id detect only-show-errors output reviewers required",
		"repos pr reviewer add --id 42 --reviewers reviewer@example.invalid",
		"Requires --id and at least one reviewer login or identity GUID.",
	},
	"repos pr reviewer remove": {
		"Remove reviewers from a pull request.",
		"organization id detect only-show-errors output reviewers",
		"repos pr reviewer remove --id 42 --reviewers reviewer@example.invalid",
		"Requires --id and at least one reviewer login or identity GUID.",
	},
	"boards work-item create": {
		"Create an Azure Boards work item.",
		"organization project detect only-show-errors output type title description fields",
		"boards work-item create --type Bug --title \"Fix issue\" --fields \"System.Tags=triage; cli\"",
		"Requires --type, --title and a project. The project can come from the checkout. Text and field values accept @FILE or @- for stdin.",
	},
	"boards work-item update": {
		"Update an Azure Boards work item by ID.",
		"organization id detect only-show-errors output title description state fields",
		"boards work-item update --id 42 --title \"Updated title\"",
		"Requires --id. Text and field values accept @FILE or @- for stdin.",
	},
	"boards work-item show": {
		"Read an Azure Boards work item by ID.",
		"organization id detect only-show-errors output expand",
		"boards work-item show --id 42 --expand relations",
		"Requires --id. Use --expand relations to include linked work items and pull requests.",
	},
	"devops login": {
		"Verify a PAT for a configured organization and save it in the OS keyring. Reads the PAT from a hidden prompt or stdin.",
		"organization",
		"devops login --organization https://engineering-example.visualstudio.com",
		"Requires --organization with a configured alias or collection URL.",
	},
	"devops invoke": {
		"List the selected collection's REST resources or call a resource by area and name.",
		"organization detect only-show-errors output area resource api-version route-parameters query-parameters http-method in-file media-type accept-media-type out-file query encoding",
		"devops invoke --area core --resource projects --query 'value[].name' -o tsv",
		"Run without --resource to list the catalog, optionally filtered by --area. To call a resource, specify its area and name and supply route parameters as KEY=VALUE. Use --in-file for a JSON request body or --out-file for a raw response.",
	},
	"devops team list": {
		"List teams in an Azure DevOps project.",
		"organization project detect only-show-errors output top skip",
		"devops team list --project Project",
		"Requires a project. The project can come from the checkout.",
	},
	"devops team list-member": {
		"List members of a team in an Azure DevOps project.",
		"organization project detect only-show-errors output team top skip",
		"devops team list-member --project Project --team \"Project Team\" --top 20",
		"Requires --team and a project. The project can come from the checkout.",
	},
	"rest": {
		"Call an API using a configured collection URL or a collection-relative path in a recognized checkout.",
		"url method body headers uri-parameters output-file output query only-show-errors",
		"rest --url '/_apis/projects?api-version=7.1' --query 'value[].name' -o tsv",
		"Requires --url. Full URLs can use a configured collection or organization alias. Use --body @FILE to read a request body and --output-file to save a raw response.",
	},
}

var optionNames = map[string]string{
	"--uri": "url", "-u": "url", "-m": "method", "-b": "body", "--url-parameters": "uri-parameters",
	"--org": "organization", "-o": "output", "-p": "project", "-r": "repository", "-i": "id",
}

type optionKind int

const (
	valueOption optionKind = iota
	flagOption
	listOption
)

func azureOptionKind(name string) optionKind {
	switch name {
	case "include-links", "only-show-errors":
		return flagOption
	case "route-parameters", "query-parameters", "uri-parameters", "headers", "fields", "reviewers":
		return listOption
	default:
		return valueOption
	}
}

func azureOptionValue(topic, name string) string {
	switch name {
	case "detect", "draft", "squash", "auto-complete", "delete-source-branch", "transition-work-items", "required":
		return "true|false"
	case "output":
		switch topic {
		case "account show":
			return "json|tsv"
		case "devops invoke", "rest":
			return "json|tsv|table|none"
		default:
			return "json"
		}
	case "status":
		if topic == "repos pr update" {
			return "active|abandoned|completed"
		}
		return "active|abandoned|completed|all"
	case "expand":
		return "none|relations|fields|links|all"
	case "method", "http-method":
		return "GET|HEAD|OPTIONS|POST|PUT|PATCH|DELETE"
	case "organization", "url":
		return "URL"
	case "id", "top", "skip":
		return "NUMBER"
	case "route-parameters", "query-parameters", "fields":
		return "KEY=VALUE"
	case "reviewers", "creator", "reviewer":
		return "IDENTITY"
	case "in-file", "out-file", "output-file":
		return "PATH"
	default:
		return "VALUE"
	}
}

type nativeCommand struct {
	description string
	arguments   string
	example     string
}

// Extra help text for native commands whose one-line summary is not enough.
var nativeCommandNotes = map[string]string{
	"route setup":    "Run on the host and under the account where the browser runs. For the web client’s Browser panel this is the T3 server. Setup uses the exact registered alias names, a local leaf certificate, hosts entries and a supervised loopback listener. System integration requires OS privileges. Rerun setup after interruption, or run route restore to undo it. See https://github.com/toolspell/tfsaz/blob/main/docs/user/browser-routing.md#what-setup-installs for what setup installs and needs on each platform.",
	"route verify":   "Checks the local resolver and both loopback addresses on port 443 using the managed certificate. Open the printed alias URL in your intended browser and compare the address bar with the expected destination to confirm browser trust and routing.",
	"route restore":  "Stop using alias links before restoring public name resolution. Uses the saved installation identity for the selected configuration. After removing or adding checkout aliases, or when the certificate expires, run restore and then setup.",
	"route redirect": "Listens on 127.0.0.1 and ::1 (default port 443) and answers GET and HEAD for supported repository, PR, commit, work-item and build pages with a 302 to the real collection. The browser carries the #fragment to the destination. Fragments stay in the browser, so the redirector cannot validate them. The certificate must be an unexpired CA:FALSE serverAuth leaf whose DNS names are all exact generated aliases of active setups or profiles; wildcards, IP names and shared hosts such as dev.azure.com are refused. SNI and Host must name the same alias. Other names, methods, Git traffic, request bodies and Authorization headers are refused. Nothing is proxied or logged and no PAT is read. Each request rereads the configuration, so a restored setup stops redirecting at once. Map the alias to loopback with tfsaz route hosts and trust only this leaf. If port 443 needs elevated privileges, use a loopback port redirect to a high port and run tfsaz as the profile owner. See https://github.com/toolspell/tfsaz/blob/main/docs/user/browser-routing-manual.md for running the service yourself.",
}

const profileOptions = "[--id ID] [--organization URL] [--ca-file PATH] [--no-login]"
const collectionExample = " --collection https://devops.example.invalid/tfs/DefaultCollection"

var nativeCommands = map[string]nativeCommand{
	"init": {
		"Set up a collection profile and verify its PAT. Use --no-login to configure the profile without reading a PAT.",
		"[--collection URL] " + profileOptions,
		"init" + collectionExample,
	},
	"profile add": {
		"Add a collection profile to the end of the discovery order.",
		"--collection URL " + profileOptions,
		"profile add" + collectionExample + " --id engineering --no-login",
	},
	"profile list": {
		"List configured collection profiles in discovery order.",
		"",
		"profile list",
	},
	"profile logout": {
		"Remove the saved PAT for a profile.",
		"--profile ID",
		"profile logout --profile engineering",
	},
	"doctor": {
		"Check the CLI, collection profiles, credentials and current or specified checkout.",
		"[PATH] [--json]",
		"doctor --json",
	},
	"shim install": {
		"Install az and git-remote-tfsaz in a directory on T3's PATH. Windows installs executable copies when symbolic links are unavailable.",
		"--dir DIR",
		"shim install --dir ./bin",
	},
	"repo setup": {
		"Set up a checkout for T3. New setups preserve the original HTTPS or SSH transport. Use --managed-ssh for the SSH wrapper, and --ssh-url to override its native endpoint. --ssh-url also selects that mode on its own.",
		"[PATH] [--profile ID] [--preserve-transport | --managed-ssh] [--ssh-url URL]",
		"repo setup ./checkout",
	},
	"repo show": {
		"Show the saved repository binding and transport without contacting the collection.",
		"[PATH]",
		"repo show ./checkout",
	},
	"repo verify": {
		"Check managed Git settings and read access for one checkout or all registered checkouts.",
		"[PATH]",
		"repo verify ./checkout",
	},
	"repo list": {
		"List managed repository bindings.",
		"[PATH]",
		"repo list",
	},
	"repo restore": {
		"Restore the checkout's saved Git settings offline.",
		"[PATH]",
		"repo restore ./checkout",
	},
	"route setup": {
		"Configure browser redirects for this configuration’s registered repository aliases on this machine.",
		"",
		"route setup",
	},
	"route verify": {
		"Check loopback resolution and the HTTPS redirect on IPv4 and IPv6. Supply a page URL for a browser-specific check.",
		"[HTTPS_PAGE_URL]",
		"route verify",
	},
	"route restore": {
		"Remove the machine browser routing installed for this configuration.",
		"",
		"route restore",
	},
	"route explain": {
		"Describe how tfsaz routes an Azure DevOps web link.",
		"URL",
		"route explain https://engineering-example.visualstudio.com/Project/_git/Repo/pullrequest/42",
	},
	"route resolve": {
		"Print the native destination for an Azure DevOps web link.",
		"URL",
		"route resolve https://engineering-example.visualstudio.com/Project/_git/Repo/pullrequest/42",
	},
	"route handler-export": {
		"Export a desktop HTTP/HTTPS handler for the current platform. Use an app bundle and browser bundle ID on macOS, a new folder and browser .exe on Windows, or a .desktop file and browser executable on Linux.",
		"DESTINATION BROWSER",
		desktopHandlerExample,
	},
	"route handler-open": {
		"Open one HTTP or HTTPS link using an exported handler’s config file and browser executable.",
		"CONFIG BROWSER URL",
		"route handler-open CONFIG BROWSER https://example.org/",
	},
	"route mac-handler-export": {
		"Export a macOS link handler that opens routed URLs in the specified browser.",
		"NEW.app BROWSER_BUNDLE_ID",
		"route mac-handler-export 'tfsaz Links.app' com.apple.Safari",
	},
	"route browser-target": {
		"Verify and print the browser destination for a URL.",
		"URL",
		"route browser-target https://engineering-example.visualstudio.com/Project/_git/Repo/pullrequest/42",
	},
	"route verify-node": {
		"Check Node's TLS trust for configured collections.",
		"",
		"route verify-node",
	},
	"route browser-export": {
		"Export a Chromium extension that routes configured Azure DevOps URLs.",
		"NEW_DIRECTORY",
		"route browser-export ./tfsaz-browser",
	},
	"route browser-verify": {
		"Check an exported browser extension against the current configuration.",
		"DIRECTORY",
		"route browser-verify ./tfsaz-browser",
	},
	"route run": {
		"Launch a program with tfsaz HTTPS routing for Git in its process tree.",
		"-- PROGRAM [ARG ...]",
		"route run -- git clone https://engineering-example.visualstudio.com/Project/_git/Repo",
	},
	"route hosts": {
		"Print loopback hosts entries for registered Azure-style hostnames.",
		"[--include-dev-azure]",
		"route hosts",
	},
	"route verify-hosts": {
		"Check whether registered Azure-style hostnames resolve to loopback.",
		"[--include-dev-azure]",
		"route verify-hosts",
	},
	"route sink": {
		"Run a local TCP listener that refuses connections.",
		"[--port PORT]",
		"route sink --port 8443",
	},
	"route redirect": {
		"Redirect browser links for registered tfsaz-*.visualstudio.com aliases on loopback.",
		"--cert FILE --key FILE [--port PORT]",
		"route redirect --cert alias.pem --key alias-key.pem",
	},
	"route proxy": {
		"Serve a PAC file and refuse SOCKS5 and HTTP proxy requests for routed hosts.",
		"[--port PORT] [--include-dev-azure]",
		"route proxy --port 1080",
	},
	"route pac": {
		"Print the PAC rules for registered Azure-style hostnames.",
		"[--port PORT] [--include-dev-azure]",
		"route pac --port 1080",
	},
}

var helpGroups = map[string]string{
	"":                  "tfsaz — Azure DevOps Server for T3 Code",
	"account":           "Read a collection's authenticated identity.",
	"boards":            "Work with Azure Boards.",
	"boards work-item":  "Read and write Azure Boards work items.",
	"devops":            "Authenticate and access Azure DevOps Server.",
	"devops team":       "Read Azure DevOps teams and members.",
	"profile":           "Manage collection profiles and credentials.",
	"repo":              "Manage local Git checkouts for T3.",
	"repos":             "Work with Azure DevOps Git repositories.",
	"repos pr":          "Work with Azure DevOps pull requests.",
	"repos pr policy":   "Read pull request policy evaluations.",
	"repos pr reviewer": "Manage pull request reviewers.",
	"route":             "Route Azure-style URLs to Azure DevOps Server.",
	"shim":              "Install launchers for T3 and Git.",
}

func commandWords(args []string) int {
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") {
		i++
	}
	return i
}

func helpTopic(args []string) (string, bool) {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return "", true
	}
	for i, arg := range args {
		if arg == "--" {
			break
		}
		if arg != "--help" && arg != "-h" {
			continue
		}
		words := args[:commandWords(args[:i])]
		topic := strings.Join(words, " ")
		if helpDescription(topic) != "" && topic != "" {
			return topic, true
		}
		// Native leaf commands can take a path or URL before their help option.
		for n := len(words) - 1; n > 0; n-- {
			topic := strings.Join(words[:n], " ")
			if command, ok := nativeCommands[topic]; ok && command.acceptsPositionals() {
				return topic, true
			}
		}
		break
	}
	return "", false
}

func (c nativeCommand) acceptsPositionals() bool {
	// Native usage puts any leading positional arguments before options.
	return c.arguments != "" && !strings.HasPrefix(strings.TrimPrefix(c.arguments, "["), "--")
}

func helpDescription(topic string) string {
	if command, ok := azureCommands[topic]; ok {
		return command.description
	}
	if command, ok := nativeCommands[topic]; ok {
		return command.description
	}
	return helpGroups[topic]
}

func writeHelp(w io.Writer, topic string) error {
	var b strings.Builder
	fmt.Fprintln(&b, helpDescription(topic))
	if command, ok := azureCommands[topic]; ok {
		fmt.Fprintf(&b, "\nUsage: tfsaz %s [OPTIONS]\n\n%s\n", topic, command.notes)
		if topic != "devops login" && topic != "rest" && topic != "account show" && topic != "repos pr checkout" {
			fmt.Fprintln(&b, "Use --organization to select a collection explicitly, or run from a recognized checkout.")
		}
		fmt.Fprintln(&b, "\nOptions:")
		for _, name := range strings.Fields(command.options) {
			aliases := []string{"--" + name}
			for alias, target := range optionNames {
				if target == name {
					aliases = append(aliases, alias)
				}
			}
			slices.Sort(aliases[1:])
			var value string
			switch azureOptionKind(name) {
			case valueOption:
				value = " " + azureOptionValue(topic, name)
			case listOption:
				label := azureOptionValue(topic, name)
				value = " " + label + " [" + label + " ...]"
			}
			fmt.Fprintf(&b, "  %s%s\n", strings.Join(aliases, ", "), value)
		}
		fmt.Fprintf(&b, "\nExamples:\n  tfsaz %s\n", command.example)
		if topic == "account show" {
			fmt.Fprintln(&b, "  tfsaz account show --query user -o json --organization https://engineering-example.visualstudio.com")
		} else if topic == "devops invoke" {
			fmt.Fprintln(&b, "  tfsaz devops invoke --area build --query '[].[resourceName,routeTemplate]' -o tsv")
		}
	} else if command, ok := nativeCommands[topic]; ok {
		fmt.Fprintf(&b, "\nUsage: tfsaz %s\n", strings.TrimSpace(topic+" "+command.arguments))
		if notes := nativeCommandNotes[topic]; notes != "" {
			fmt.Fprintf(&b, "\n%s\n", notes)
		}
		fmt.Fprintf(&b, "\nExample:\n  tfsaz %s\n", command.example)
	} else {
		prefix, command := "", "tfsaz"
		if topic != "" {
			prefix, command = topic+" ", "tfsaz "+topic
		}
		fmt.Fprintf(&b, "\nUsage: %s COMMAND [OPTIONS]\n\nCommands:\n", command)
		var children []string
		addChild := func(name string) {
			if child, ok := strings.CutPrefix(name, prefix); ok && child != "" && !strings.ContainsRune(child, ' ') {
				children = append(children, child)
			}
		}
		for name := range helpGroups {
			addChild(name)
		}
		for name := range azureCommands {
			addChild(name)
		}
		for name := range nativeCommands {
			addChild(name)
		}
		slices.Sort(children)
		for _, name := range children {
			fmt.Fprintf(&b, "  %-19s %s\n", name, helpDescription(prefix+name))
		}
		fmt.Fprintf(&b, "\nRun %s COMMAND --help for examples and options.\n", command)
		if len(children) != 0 {
			fmt.Fprintf(&b, "\nExample:\n  %s %s --help\n", command, children[0])
		}
		if topic == "" {
			fmt.Fprintln(&b, "\nUse tfsaz --version to print the version. Set an absolute TFSAZ_CONFIG to select a separate configuration file.")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
