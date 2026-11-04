package hub

import (
	"strings"
	"unicode/utf8"
)

var allowedRootExtensions = []string{
	".md",
	".png",
	".jpg",
	".jpeg",
	".gif",
	".webp",
	".svg",
	".heic",
}

var reservedRootFiles = []string{
	"index.md",
	"log.md",
	"schema.md",
	"curator-memory.md",
}

// CheckPath checks whether p is an allowed relative path in the vault.
// Returns nil if allowed, or *Error with CodeBadRequest or CodeOutOfScope.
func CheckPath(p string) error {
	if p == "" {
		return &Error{Code: CodeBadRequest, Message: "path cannot be empty"}
	}
	if len(p) > 512 {
		return &Error{Code: CodeBadRequest, Message: "path exceeds 512 bytes"}
	}
	if !utf8.ValidString(p) {
		return &Error{Code: CodeBadRequest, Message: "path must be valid UTF-8"}
	}
	if strings.ContainsRune(p, 0) {
		return &Error{Code: CodeBadRequest, Message: "path cannot contain NUL byte"}
	}
	if strings.ContainsRune(p, '\\') {
		return &Error{Code: CodeBadRequest, Message: "path cannot contain backslash"}
	}
	if strings.HasPrefix(p, "/") {
		return &Error{Code: CodeBadRequest, Message: "path cannot be absolute"}
	}
	if strings.HasSuffix(p, "/") {
		return &Error{Code: CodeBadRequest, Message: "path cannot have trailing slash"}
	}
	if strings.Contains(p, "//") {
		return &Error{Code: CodeBadRequest, Message: "path cannot contain empty segment"}
	}

	segments := strings.Split(p, "/")
	for _, seg := range segments {
		if seg == "." || seg == ".." {
			return &Error{Code: CodeBadRequest, Message: "path cannot contain . or .. segments"}
		}
		if strings.HasPrefix(seg, ".") {
			return &Error{Code: CodeOutOfScope, Message: "hidden segments not allowed"}
		}
	}

	if strings.HasPrefix(p, "notes/") || strings.HasPrefix(p, "ledger/") {
		return nil
	}

	// If it has multiple segments and is not under notes/ or ledger/, it is out of scope.
	if len(segments) > 1 {
		return &Error{Code: CodeOutOfScope, Message: "path outside notes/ and ledger/"}
	}

	// Single root segment: check reserved files
	for _, reserved := range reservedRootFiles {
		if strings.EqualFold(p, reserved) {
			return &Error{Code: CodeOutOfScope, Message: "root file is reserved"}
		}
	}

	// Single root segment: check allowed extensions
	lower := strings.ToLower(p)
	for _, ext := range allowedRootExtensions {
		if strings.HasSuffix(lower, ext) {
			return nil
		}
	}

	return &Error{Code: CodeOutOfScope, Message: "root file extension not allowed"}
}
