package settings

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/jpeg"
	"image/png"
)

// errNotRaster is the refusal of bytes that are not a PNG, JPEG or WebP image.
var errNotRaster = errors.New("must be a PNG, JPEG or WebP image")

// RasterImage reads the media type and the size in pixels of a PNG, JPEG or WebP image from its
// bytes, never from a file name. Anything else, an SVG included, is an error.
func RasterImage(b []byte) (contentType string, width, height int, err error) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		c, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return "", 0, 0, errNotRaster
		}
		return "image/png", c.Width, c.Height, nil
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		c, err := jpeg.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return "", 0, 0, errNotRaster
		}
		return "image/jpeg", c.Width, c.Height, nil
	case len(b) >= 30 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		w, h, ok := webpSize(b)
		if !ok {
			return "", 0, 0, errNotRaster
		}
		return "image/webp", w, h, nil
	}
	return "", 0, 0, errNotRaster
}

// webpSize reads the canvas size from the first chunk of a WebP file: VP8 (lossy), VP8L
// (lossless) or VP8X (extended). b holds at least 30 bytes.
func webpSize(b []byte) (int, int, bool) {
	d := b[20:] // the data of the first chunk
	switch string(b[12:16]) {
	case "VP8 ":
		// A key frame: the 3-byte frame tag, the start code 9d 01 2a, then 14-bit width and height.
		if d[3] != 0x9d || d[4] != 0x01 || d[5] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(d[6:]) & 0x3fff), int(binary.LittleEndian.Uint16(d[8:]) & 0x3fff), true
	case "VP8L":
		// The signature 0x2f, then 14 bits of width-1 and 14 bits of height-1.
		if d[0] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(d[1:])
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, true
	case "VP8X":
		// Flags and reserved bytes, then 24 bits of canvas width-1 and 24 bits of height-1.
		w := int(d[4]) | int(d[5])<<8 | int(d[6])<<16
		h := int(d[7]) | int(d[8])<<8 | int(d[9])<<16
		return w + 1, h + 1, true
	}
	return 0, 0, false
}
