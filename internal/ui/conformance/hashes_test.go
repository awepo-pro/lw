package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// envVault and envGrids name the gate's two read-only inputs (contract §9
// note 1): the private mockup vault, and the frozen grids directory.
const (
	envVault = "LW_MOCKUP_VAULT"
	envGrids = "LW_MOCKUP_GRIDS"
)

// skipReason is the exact skip both tests report without the vault.
const skipReason = "LW_MOCKUP_VAULT not set: the frozen-grid gate runs locally against the private mockup vault"

// gridSHA256 is MASTER §5.0's frozen grid table, embedded as a literal map
// (contract §9 note 2). Every grid file is verified against it before any
// comparison runs: a modified grid is a broken gate, not a failing screen.
var gridSHA256 = map[string]string{
	"ask-80x24.txt":               "cf176365bd40aec854c5b01e39a370d852675aabf0bd5586438e0b2bf1062920",
	"ask-100x30.txt":              "43d6acf0e94fbd3450ceb99352a1eda82ded51534aa503c9735385b1794e28bf",
	"ask-120x40.txt":              "8e3b5b8d7fa34bde8f4d38fa0c064aa26db95e8df8474bfcd7c88497c1cd00ce",
	"ask-200x60.txt":              "59070f7cec1228e47a6a501ea3bb75874be51c64bad46b8a5fc98c78d0fc903f",
	"ask-conversation-80x24.txt":  "95f8dc3e3481199e136e07c37438c7229102435d89e60945aba0f016171ffef7",
	"ask-conversation-100x30.txt": "5bc5fec85e07599df292e297f069c06a5e60558bcca295523c8b7c9b4f280e32",
	"ask-conversation-120x40.txt": "28147839aaf1d750da1729a352bb4d5888e2be8dccc6f210952808bda20b6ea3",
	"ask-conversation-200x60.txt": "b307a19aebcd79d3b7947da122bbdde1cd79329142fa4cb19abdb639c095284a",
	"browse-80x24.txt":            "68fa572258cd5a279799a7f0baab85c6c0a48a4a406b106f437249ae8334e8e3",
	"browse-100x30.txt":           "e71b0f99cb61b5ab01f0a1666b76ff2ffaa77f44042c7fd9f50ab50149e64919",
	"browse-120x40.txt":           "cfc93170a8e09252f37017991c46a8060864943301783583b3917158e8e2bf46",
	"browse-200x60.txt":           "ec0130410434cac7cfb511415bf3ed2719204792b183364082faa1bd3babd1e5",
	"keys-80x24.txt":              "e92a39d09110527b7a1460a95c104e3b70b8ffbfcb279e2153e31fae73572855",
	"keys-100x30.txt":             "ea8657b0529fa7eed9397fc56ed147c925c24327fd38b05bcdd06fafbeb3e929",
	"keys-120x40.txt":             "fd62d25dc01e467d8c72c0781a028f9991402b4a0adbc1427a604d640fedaf2a",
	"keys-200x60.txt":             "a5ec11fb22034bba96a88bfd0751bec3da05fc119ed83e7d5aae23d0efc565b4",
	"review-80x24.txt":            "6469945b2ae13c419c5ca8f68a042d5cc5bb99e5f0b6afcc260c98973bc19244",
	"review-100x30.txt":           "f7cf92ec96a9e8ba182d40af8300676a7ea7e38379fdb607f2ec990a333df9d6",
	"review-120x40.txt":           "96c9de5c9644de6b9edaba5a796166980710dfb35dfd5b0c7aaadb252354d599",
	"review-200x60.txt":           "a07dd43bd9812888885fa8607f5c8ea3ddb37da21ef16cab56873d1488fb3a0a",
	"review-preview-80x24.txt":    "a5659c8ec6124380b09b2ac5fc8c6ebb9768fb56b205c2bc4be88535b41b5eb8",
	"review-preview-100x30.txt":   "11c6ee5d50234d930d10f016e9f5bab5bf2c5847ce6c4a8947c5e5bf07ca24c7",
	"review-preview-120x40.txt":   "c9f7de1fbea5a3b1afb3303e927537bf9a90f89e8172c886b4872343ab0f14be",
	"review-preview-200x60.txt":   "fdef51f273a88af3f945791ef363623538cae14f679eac4e9a4fe88600a0c166",
	"too-small-120x20.txt":        "95d3ac161df96559d23e76b0836c7473b8441f6461494c5d1780c7f1bc4b3f5e",
	"too-small-72x20.txt":         "3f6f00920824875912247bbd1d5a8c2f072e3ef1ddeec3ff88098e880aacbddc",
}

// gridsDir resolves the frozen grids directory: $LW_MOCKUP_GRIDS, or, by
// default, the metadata tree's plans/003-mockups/ascii, five levels above
// the private vault (contract §9 note 1).
func gridsDir(vault string) string {
	if dir := os.Getenv(envGrids); dir != "" {
		return dir
	}
	return filepath.Join(vault, "..", "..", "..", "..", "..", "plans", "003-mockups", "ascii")
}

// loadGrids reads every frozen grid from dir and verifies its sha256
// against gridSHA256, in the table's sorted name order so any failure is
// deterministic. The returned map holds each grid's lines, with the file's
// single trailing newline (the comparison target is plain + "\n") stripped.
func loadGrids(t *testing.T, dir string) map[string][]string {
	t.Helper()

	grids := make(map[string][]string, len(gridSHA256))
	for _, name := range sortedGridNames() {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("setup: frozen grid %s: %v", name, err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != gridSHA256[name] {
			t.Fatalf("setup: frozen grid %s was modified", name)
		}
		grids[name] = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
	return grids
}

// sortedGridNames lists gridSHA256's keys in byte order.
func sortedGridNames() []string {
	names := make([]string, 0, len(gridSHA256))
	for name := range gridSHA256 {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
