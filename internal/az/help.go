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
}

// The Azure-compatible parser and offline help use the same option sets.
var azureCommands = map[string]azureCommand{
	"account show": {
		"Read the authenticated user. The user.name TSV probe without an organization checks profiles in configuration order.",
		"query output organization only-show-errors",
		"account show --query user.name -o tsv",
	},
	"repos show": {
		"Read an Azure DevOps Git repository.",
		"organization project repository detect only-show-errors output",
		"repos show --project Project --repository Repo",
	},
	"repos create": {
		"Create a Git repository in an Azure DevOps project.",
		"organization project detect name only-show-errors output",
		"repos create --project Project --name Repo",
	},
	"repos pr list": {
		"List pull requests for a project or repository.",
		"organization project repository detect only-show-errors output top skip source-branch target-branch status include-links creator reviewer",
		"repos pr list --status active --top 10",
	},
	"repos pr show": {
		"Read a pull request by ID.",
		"organization id detect only-show-errors output",
		"repos pr show --id 42",
	},
	"repos pr policy list": {
		"Read one page of policy evaluations for a pull request.",
		"organization id detect only-show-errors output top skip",
		"repos pr policy list --id 42 --top 100 --skip 0",
	},
	"repos pr checkout": {
		"Fetch and switch to a pull request branch in the current repository.",
		"organization id remote-name detect only-show-errors output",
		"repos pr checkout --id 42 --remote-name origin",
	},
	"repos pr create": {
		"Create a pull request.",
		"organization project repository detect only-show-errors output source-branch target-branch title description draft squash auto-complete delete-source-branch merge-commit-message transition-work-items",
		"repos pr create --source-branch feature --target-branch main --title \"Add feature\"",
	},
	"repos pr update": {
		"Update a pull request by ID.",
		"organization id detect only-show-errors output title description status squash auto-complete draft delete-source-branch merge-commit-message transition-work-items",
		"repos pr update --id 42 --title \"Updated title\"",
	},
	"repos pr reviewer add": {
		"Add reviewers to a pull request.",
		"organization id detect only-show-errors output reviewers required",
		"repos pr reviewer add --id 42 --reviewers reviewer@example.invalid",
	},
	"repos pr reviewer remove": {
		"Remove reviewers from a pull request.",
		"organization id detect only-show-errors output reviewers",
		"repos pr reviewer remove --id 42 --reviewers reviewer@example.invalid",
	},
	"boards work-item create": {
		"Create an Azure Boards work item.",
		"organization project detect only-show-errors output type title description fields",
		"boards work-item create --type Bug --title \"Fix issue\"",
	},
	"boards work-item update": {
		"Update an Azure Boards work item by ID.",
		"organization id detect only-show-errors output title description state fields",
		"boards work-item update --id 42 --title \"Updated title\"",
	},
	"boards work-item show": {
		"Read an Azure Boards work item by ID.",
		"organization id detect only-show-errors output expand",
		"boards work-item show --id 42 --expand relations",
	},
	"devops login": {
		"Verify a PAT for a configured organization and save it in the OS keyring. Reads the PAT from a hidden prompt or stdin.",
		"organization",
		"devops login --organization https://engineering-example.visualstudio.com",
	},
	"devops invoke": {
		"List the selected collection's REST resources or call a resource by area and name.",
		"organization detect only-show-errors output area resource api-version route-parameters query-parameters http-method in-file media-type accept-media-type out-file query encoding",
		"devops invoke --area core --resource projects --query 'value[].name' -o tsv",
	},
	"devops team list": {
		"List teams in an Azure DevOps project.",
		"organization project detect only-show-errors output top skip",
		"devops team list --project Project",
	},
	"devops team list-member": {
		"List members of a team in an Azure DevOps project.",
		"organization project detect only-show-errors output team top skip",
		"devops team list-member --project Project --team Team",
	},
	"rest": {
		"Call an API using a configured collection URL or a collection-relative path in a recognized checkout.",
		"url method body headers uri-parameters output-file output query only-show-errors",
		"rest --url '/_apis/projects?api-version=7.1' --query 'value[].name' -o tsv",
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

type nativeCommand struct {
	description string
	arguments   string
	example     string
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
		"[--json] [PATH]",
		"doctor --json",
	},
	"shim install": {
		"Install the az and git-remote-tfsaz launchers in a directory on T3's PATH.",
		"--dir DIR",
		"shim install --dir ./bin",
	},
	"repo setup": {
		"Set up a checkout for T3. New setups preserve the original HTTPS or SSH transport. Use --managed-ssh to select the SSH wrapper.",
		"[PATH] [--profile ID] [--preserve-transport | --managed-ssh [--ssh-url URL]]",
		"repo setup ./checkout --preserve-transport",
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
			if _, ok := nativeCommands[topic]; ok {
				return topic, true
			}
		}
		break
	}
	return "", false
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
		fmt.Fprintf(&b, "\nUsage: tfsaz %s [OPTIONS]\n\nOptions:\n", topic)
		for _, name := range strings.Fields(command.options) {
			aliases := []string{"--" + name}
			for alias, target := range optionNames {
				if target == name {
					aliases = append(aliases, alias)
				}
			}
			slices.Sort(aliases[1:])
			value := " VALUE"
			switch azureOptionKind(name) {
			case flagOption:
				value = ""
			case listOption:
				value = " VALUE [VALUE ...]"
			}
			fmt.Fprintf(&b, "  %s%s\n", strings.Join(aliases, ", "), value)
		}
		fmt.Fprintf(&b, "\nExamples:\n  tfsaz %s\n", command.example)
		if topic == "account show" {
			fmt.Fprintln(&b, "  tfsaz account show --query user -o json")
		}
	} else if command, ok := nativeCommands[topic]; ok {
		fmt.Fprintf(&b, "\nUsage: tfsaz %s\n\nExample:\n  tfsaz %s\n", strings.TrimSpace(topic+" "+command.arguments), command.example)
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
