package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// completionSubcommands maps verbs that take subcommands to those subcommands.
var completionSubcommands = map[string][]string{
	"note":       {"list"},
	"session":    {"list", "show"},
	"trace":      {"show"},
	"sync":       {"status", "init", "clone"},
	"storage":    {"add", "list", "show", "get", "rm"},
	"ledger":     {"check", "sum", "import"},
	"completion": {"bash", "zsh", "fish"},
}

// verbNamesProvider yields the list of verb names. Set in init() to avoid an
// initialization cycle between the package-level verbs table and cmdCompletion.
var verbNamesProvider func() []string

func init() {
	verbNamesProvider = func() []string {
		names := make([]string, 0, len(verbs))
		for _, v := range verbs {
			names = append(names, v.name)
		}
		return names
	}
}

// allVerbNames returns the names of all registered verbs from the verbs table.
func allVerbNames() []string {
	if verbNamesProvider != nil {
		return verbNamesProvider()
	}
	return nil
}

// cmdCompletion prints shell completion scripts for bash, zsh, or fish.
//
// Usage: lw completion bash|zsh|fish
// Exit 0 on success, 2 on usage errors.
func cmdCompletion(args []string) error {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: lw completion bash|zsh|fish")
		return &exitError{code: 2}
	}

	shell := args[0]
	vNames := allVerbNames()

	switch shell {
	case "bash":
		return writeBashCompletion(os.Stdout, vNames)
	case "zsh":
		return writeZshCompletion(os.Stdout, vNames)
	case "fish":
		return writeFishCompletion(os.Stdout, vNames)
	default:
		fmt.Fprintf(os.Stderr, "lw completion: unknown shell %q; expected bash, zsh, or fish\n", shell)
		return &exitError{code: 2}
	}
}

func writeBashCompletion(w io.Writer, verbList []string) error {
	verbsStr := strings.Join(verbList, " ")

	var cases []string
	for _, verb := range verbList {
		if subs, ok := completionSubcommands[verb]; ok {
			words := append([]string{}, subs...)
			words = append(words, "--vault", "--help")
			cases = append(cases, fmt.Sprintf("        %s)\n            COMPREPLY=( $(compgen -W \"%s\" -- \"$cur\") )\n            ;;", verb, strings.Join(words, " ")))
		}
	}

	tmpl := `_lw() {
    local cur prev words cword
    if declare -F _init_completion >/dev/null 2>&1; then
        _init_completion -n = 2>/dev/null || _init_completion || return
    else
        COMPREPLY=()
        cur="${COMP_WORDS[COMP_CWORD]}"
        prev="${COMP_WORDS[COMP_CWORD-1]}"
        words=("${COMP_WORDS[@]}")
        cword=$COMP_CWORD
    fi

    if [[ "$prev" == "--vault" ]]; then
        if declare -F _filedir >/dev/null 2>&1; then
            _filedir -d
        else
            COMPREPLY=( $(compgen -d -- "$cur") )
        fi
        return
    fi

    local verb=""
    local i
    for (( i=1; i < cword; i++ )); do
        if [[ "${words[i]}" != -* ]]; then
            verb="${words[i]}"
            break
        fi
    done

    if [[ -z "$verb" ]]; then
        if [[ "$cur" == -* ]]; then
            COMPREPLY=( $(compgen -W "--vault --help" -- "$cur") )
        else
            COMPREPLY=( $(compgen -W "%s" -- "$cur") )
        fi
        return
    fi

    case "$verb" in
%s
        *)
            if [[ "$cur" == -* ]]; then
                COMPREPLY=( $(compgen -W "--vault --help" -- "$cur") )
            fi
            ;;
    esac
}

complete -F _lw lw
`
	_, err := fmt.Fprintf(w, tmpl, verbsStr, strings.Join(cases, "\n"))
	return err
}

func writeZshCompletion(w io.Writer, verbList []string) error {
	var verbsEscaped []string
	for _, v := range verbList {
		verbsEscaped = append(verbsEscaped, fmt.Sprintf("        '%s'", v))
	}

	var cases []string
	for _, verb := range verbList {
		if subs, ok := completionSubcommands[verb]; ok {
			cases = append(cases, fmt.Sprintf("        %s)\n            _arguments -s $common_flags '1:subcommand:(%s)'\n            ;;", verb, strings.Join(subs, " ")))
		}
	}

	tmpl := `#compdef lw

_lw() {
    local -a common_flags
    common_flags=(
        '--vault[vault root]:directory:_files -/'
        '--help[print help and exit]'
    )

    if (( CURRENT == 2 )); then
        local -a verbs
        verbs=(
%s
        )
        _describe -t verbs 'lw verb' verbs
        _arguments -s $common_flags
        return
    fi

    local verb="$words[2]"
    case "$verb" in
%s
        *)
            _arguments -s $common_flags
            ;;
    esac
}

compdef _lw lw
`
	_, err := fmt.Fprintf(w, tmpl, strings.Join(verbsEscaped, "\n"), strings.Join(cases, "\n"))
	return err
}

func writeFishCompletion(w io.Writer, verbList []string) error {
	verbsStr := strings.Join(verbList, " ")

	var subLines []string
	for _, verb := range verbList {
		if subs, ok := completionSubcommands[verb]; ok {
			subLines = append(subLines, fmt.Sprintf("complete -c lw -n \"__fish_seen_subcommand_from %s\" -a \"%s\"", verb, strings.Join(subs, " ")))
		}
	}

	tmpl := `# fish completion for lw
complete -c lw -f
complete -c lw -l vault -r -a "(__fish_complete_directories)" -d "vault root"
complete -c lw -l help -d "show help"

complete -c lw -n "__fish_use_subcommand" -a "%s"

%s
`
	_, err := fmt.Fprintf(w, tmpl, verbsStr, strings.Join(subLines, "\n"))
	return err
}
