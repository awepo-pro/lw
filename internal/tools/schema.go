package tools

// Hand-written JSON Schema for each read tool's arguments object (backbone
// §6). These are constants, not package vars: 00-conventions.md §2 forbids
// mutable package-level state, and json.RawMessage is a []byte, so each
// Tool gets its own copy converted from the constant at construction time
// in NewRegistry rather than a single shared, mutable slice.
//
// `required` is kept accurate against `properties` — TestSchemasAreValidJSON
// checks this mechanically — because the model reads these schemas
// verbatim to build its tool calls.

const vaultOrientSchema = `{
  "type": "object",
  "properties": {},
  "required": [],
  "additionalProperties": false
}`

const wikiSearchSchema = `{
  "type": "object",
  "properties": {
    "q": {
      "type": "string",
      "description": "search query; matched against page titles, tags and body text"
    },
    "type": {
      "type": "string",
      "enum": ["entity", "concept", "comparison", "query", "summary"],
      "description": "restrict results to one page type"
    },
    "tags": {
      "type": "array",
      "items": {"type": "string"},
      "description": "AND filter: only pages carrying every listed tag"
    },
    "limit": {
      "type": "integer",
      "description": "maximum hits to return; defaults to 20 and is capped at 20"
    }
  },
  "required": ["q"],
  "additionalProperties": false
}`

const wikiGetSchema = `{
  "type": "object",
  "properties": {
    "page": {
      "type": "string",
      "description": "a page's vault-relative path or its filename basename, e.g. \"wiki/concepts/kv-cache.md\" or \"kv-cache\". Not the page title — wiki.search lists each result's path before the em dash."
    },
    "section": {
      "type": "string",
      "description": "the full raw heading line to return, e.g. \"## Related\"; omit to return the whole page"
    }
  },
  "required": ["page"],
  "additionalProperties": false
}`

const wikiNeighborsSchema = `{
  "type": "object",
  "properties": {
    "page": {
      "type": "string",
      "description": "a page's vault-relative path or its filename basename, e.g. \"kv-cache\". Not the page title."
    },
    "depth": {
      "type": "integer",
      "description": "hop count; clamped to 1-2, defaults to 1"
    }
  },
  "required": ["page"],
  "additionalProperties": false
}`

const wikiBacklinksSchema = `{
  "type": "object",
  "properties": {
    "page": {
      "type": "string",
      "description": "a page's vault-relative path or its filename basename, e.g. \"kv-cache\". Not the page title."
    }
  },
  "required": ["page"],
  "additionalProperties": false
}`

const rawGetSchema = `{
  "type": "object",
  "properties": {
    "source": {
      "type": "string",
      "description": "the exact vault-relative path of a raw source under raw/, e.g. \"raw/papers/leviathan-2023.md\""
    },
    "chunk": {
      "type": "integer",
      "description": "1-based chunk index for a long source; defaults to 1"
    }
  },
  "required": ["source"],
  "additionalProperties": false
}`

const wikiLintSchema = `{
  "type": "object",
  "properties": {
    "checks": {
      "type": "array",
      "items": {"type": "string"},
      "description": "lint check IDs to run, e.g. [\"link-broken\"]; omit to run all 14"
    }
  },
  "required": [],
  "additionalProperties": false
}`
