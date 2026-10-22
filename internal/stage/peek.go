package stage

import (
	"encoding/json"
	"os"
)

// PeekOpenChangeset returns the vault's open changeset read straight from disk without opening an Engine
// (067 D3: the CLI waiting note for verbs that hold no Engine). (nil, nil) when none is open.
func PeekOpenChangeset(root string) (*Changeset, error) {
	if root == "" {
		return nil, nil
	}
	openDir := changesetOpenDir(root)
	entries, err := os.ReadDir(openDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var id string
	for _, ent := range entries {
		if ent.IsDir() {
			id = ent.Name()
			break
		}
	}
	if id == "" {
		return nil, nil
	}
	b, err := os.ReadFile(openChangesetFile(openDir, id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cs Changeset
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil, err
	}
	return &cs, nil
}
