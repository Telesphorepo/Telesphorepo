package raw

import _ "embed"

// A file with a folded corner.
//
//go:embed icon.svg
var icon []byte

// Icon implements format.Iconic.
func (f *Format) Icon() []byte { return icon }

// KindIcons implements format.Iconic.
func (f *Format) KindIcons() map[string][]byte { return nil }
