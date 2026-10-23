package tools

import "strings"

// AskToolNames returns the canonical names of the read-only tools an ask turn is offered (039), sorted.
// Every one is Tool.ReadOnly. The in-process agent and every external ask path (068) read this one list.
//
// Like IngestToolNames it is read off a registry, not copied: a read-only tool
// added to NewRegistry reaches the ask set, and TestAskToolNames fails until a
// person has decided it belongs there — which changes every ask request.
func AskToolNames() []string {
	list := NewRegistry(Deps{}).List()
	names := make([]string, 0, len(list))
	for _, t := range list {
		if !t.ReadOnly || strings.HasPrefix(t.Name, "stage.") {
			continue
		}
		names = append(names, t.Name)
	}
	return names
}
