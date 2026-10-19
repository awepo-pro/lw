package tools

// IngestToolNames returns the canonical names of the tools `lw tool` offers an
// external agent that curates an ingest (063), sorted: every tool NewRegistry
// registers over a Deps with no Search provider — the set the in-process
// ingest turn is offered, which withholds web.search (056) — less stage.open,
// because `lw ingest --prepare` has already opened the changeset the agent
// works in.
//
// The list is read off a registry, not copied: a tool added to NewRegistry
// reaches the offer set (and the test that pins it fails until a person has
// looked), rather than silently missing from it. It builds a registry over
// Deps{} — no vault, no engine; no tool touches either at construction — so it
// is cheap, and each call returns a slice of its own.
func IngestToolNames() []string {
	list := NewRegistry(Deps{}).List()
	names := make([]string, 0, len(list))
	for _, t := range list {
		if t.Name == "stage.open" {
			continue
		}
		names = append(names, t.Name)
	}
	return names
}
