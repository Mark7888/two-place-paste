package clipboard

import (
	"path/filepath"
	"strings"
)

// SafeName reduces a filename that arrived from another device to something
// safe to create on this one.
//
// A filename crosses the wire inside the encrypted frame (/spec/crypto.md §6)
// and is chosen by whatever wrote the entry. It is display text: it may hold
// separators, "..", a drive letter or a leading dot, and joining it onto a
// directory unexamined is how a clipboard entry becomes a file written
// somewhere it was never meant to go. Everything but the last path element is
// dropped and the result is bounded; an unusable name becomes "clipboard".
func SafeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = name[strings.LastIndexByte(name, '/')+1:]
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == 0x7f:
			return -1
		case strings.ContainsRune(`:*?"<>|`, r):
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "clipboard"
	}
	if len(name) > 120 {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		name = name[:120-len(ext)] + ext
	}
	return name
}
