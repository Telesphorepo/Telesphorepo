package maven

import _ "embed"

// The Apache Maven feather, and the Gradle elephant for Gradle plugin markers.
//
//go:embed icon.svg
var icon []byte

//go:embed gradle.svg
var gradleIcon []byte

// Icon implements format.Iconic.
func (h *Handler) Icon() []byte { return icon }

// KindIcons implements format.Iconic: a Gradle plugin shows the Gradle elephant.
func (h *Handler) KindIcons() map[string][]byte {
	return map[string][]byte{"gradle-plugin": gradleIcon}
}
