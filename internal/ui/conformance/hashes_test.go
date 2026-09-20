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
// (contract §9 note 2): 003's eighteen grids unchanged, plus the eight
// ask-* grids of 005's approved mockup A-5-1, the four ask-conversation-*
// of which 009's approved mockup A-9-1 redrew with the file hint line. Every grid file is verified
// against it before any comparison runs: a modified grid is a broken gate,
// not a failing screen.
var gridSHA256 = map[string]string{
	"ask-80x24.txt":               "5743061ef190720705ae10612dedd82831340167531281c36faf594eee89e9b0",
	"ask-100x30.txt":              "546bcb33044f78361f41354542772a3c74f60d7adc414f51eee0ba1f32e60894",
	"ask-120x40.txt":              "42f4a041e7d65cf52127708103df53d2237fe9452b76bd46757b8f6f716cdd65",
	"ask-200x60.txt":              "52b4110f6320cb37ba7ca517ab6711ab66bb845bdac35f166bca12097da89f4e",
	"ask-conversation-80x24.txt":  "44d18ca971ff64d5a6e059155a4d6fdac9643dc0f10080fc2a4e5bd5c2842aa2",
	"ask-conversation-100x30.txt": "77f5d9b584694195d05cb380089e1a01af052545ee72c8a27a7874693a36d310",
	"ask-conversation-120x40.txt": "f373760e5c0f8657b8c80c46bd7d72e471af578485abdbf88bcd74232f78ae47",
	"ask-conversation-200x60.txt": "13d753077d4fb3201c6e249d2d537de9b08321d2d767dce0aad0ef55c777a640",
	"browse-80x24.txt":            "68fa572258cd5a279799a7f0baab85c6c0a48a4a406b106f437249ae8334e8e3",
	"browse-100x30.txt":           "e71b0f99cb61b5ab01f0a1666b76ff2ffaa77f44042c7fd9f50ab50149e64919",
	"browse-120x40.txt":           "cfc93170a8e09252f37017991c46a8060864943301783583b3917158e8e2bf46",
	"browse-200x60.txt":           "ec0130410434cac7cfb511415bf3ed2719204792b183364082faa1bd3babd1e5",
	"keys-80x24.txt":              "86bf3a81367c92a1bae3fe900c335bc0e262d8b7a7d782f7b226fb3fe03748de",
	"keys-100x30.txt":             "799aa28027e0465a5df6b58df2c88cbabb0c793624e787bd182dfbd7073a5846",
	"keys-120x40.txt":             "6782dd904a72fa9bf4c582959a75f7bacabee34818bb75c567742ff817063773",
	"keys-200x60.txt":             "d2347c345ad49f2c1aaa705537ae691579542a79bb33be272cfff054431eb15c",
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
// default, the metadata tree's plans/009-mockups/ascii — workflow 009's
// full approved set (005's 26 grids, with A-9-1's four ask-conversation-*
// redrawn), five levels above the private vault (contract §9 note 1).
func gridsDir(vault string) string {
	if dir := os.Getenv(envGrids); dir != "" {
		return dir
	}
	return filepath.Join(vault, "..", "..", "..", "..", "..", "plans", "009-mockups", "ascii")
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
