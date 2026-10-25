package maven

import (
	"encoding/xml"
	"io"

	"golang.org/x/net/html/charset"
)

// decodeXML decodes the XML document in r into v in the charset that its declaration names.
// Maven files name others than UTF-8 (the Gradle Plugin Portal's maven-metadata.xml is US-ASCII,
// many poms on Maven Central are ISO-8859-1), and encoding/xml refuses them without a
// CharsetReader.
func decodeXML(r io.Reader, v any) error {
	d := xml.NewDecoder(r)
	d.CharsetReader = charset.NewReaderLabel
	return d.Decode(v)
}
