package debian

import _ "embed"

// The Debian swirl (Debian Open Use Logo License).
//
//go:embed icon.svg
var icon []byte

// Icon implements format.Iconic.
func (h *Handler) Icon() []byte { return icon }

// KindIcons implements format.Iconic.
func (h *Handler) KindIcons() map[string][]byte { return nil }
