package stage

// PersistedPayloadTypes hands the external fingerprint test
// (format_fingerprint_test.go, package stage_test) the unexported types the
// engine marshals into a journal event's Data field. They are not part of
// the package's API — a caller only ever sees them as json.RawMessage — but
// they are part of the vault's on-disk shape, so TestFormatFingerprint has
// to reflect over the real types rather than a copy that could drift (042).
var PersistedPayloadTypes = []any{
	commitEndPayload{},
	revertedPayload{},
}
