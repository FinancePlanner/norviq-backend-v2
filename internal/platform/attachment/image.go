package attachment

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif" // registers the GIF decoder
	"image/jpeg"
	"image/png"

	_ "golang.org/x/image/webp" // registers the WebP decoder
)

const (
	// MaxImageBytes is the largest accepted image upload.
	MaxImageBytes = 5 << 20
	// MaxImageSide is the largest accepted width or height, in pixels.
	MaxImageSide = 4096

	jpegQuality = 90

	// maxPixels is the budget for 8-bit baseline images (4096 x 4096).
	maxPixels = MaxImageSide * MaxImageSide
	// maxHeavyPixels is the budget for 16-bit PNGs and progressive JPEGs, whose
	// decoders hold far more memory per pixel (2048 x 2048).
	maxHeavyPixels = 2048 * 2048
)

// decodeSlot lets one decode run at a time, process-wide, so a few large
// uploads cannot exhaust memory together. Callers block rather than fail.
var decodeSlot = make(chan struct{}, 1)

func isSupportedImage(sniffed string) bool {
	switch sniffed {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	}
	return false
}

var errBadJPEG = errors.New("jpeg: unparseable marker stream")

// isHeavy reports a 16-bit PNG or a progressive JPEG, read from the header.
// For a JPEG it walks the markers before the first SOS the way the decoder
// does, and fails closed: a byte that is not 0xFF where a marker is expected,
// or FF 00, is errBadJPEG, so a header the walk cannot follow never slips past
// as "not progressive".
func isHeavy(data []byte) (bool, error) {
	if len(data) > 24 && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return data[24] == 16, nil // IHDR bit depth
	}
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return false, nil
	}
	i := 2
	for i < len(data) {
		if data[i] != 0xFF {
			return false, errBadJPEG
		}
		for i < len(data) && data[i] == 0xFF { // fill bytes
			i++
		}
		if i >= len(data) {
			return false, nil // truncated: the decoder will refuse it
		}
		m := data[i]
		i++
		switch {
		case m == 0x00:
			return false, errBadJPEG
		case m == 0x01 || m == 0xD8 || (m >= 0xD0 && m <= 0xD7):
			continue // standalone markers carry no length
		case m == 0xD9 || m == 0xDA:
			return false, nil // EOI, or the first SOS with no progressive SOF before it
		}
		if i+2 > len(data) {
			return false, nil
		}
		segLen := int(data[i])<<8 | int(data[i+1])
		if segLen < 2 {
			return false, errBadJPEG
		}
		if m == 0xC2 || m == 0xC6 || m == 0xCA || m == 0xCE {
			return true, nil
		}
		i += segLen
	}
	return false, nil
}

// normalizeImage decodes and re-encodes data so metadata, trailing bytes and
// animation frames are dropped. The dimensions are checked from the header
// before any pixel is decoded, so a decompression bomb is never allocated.
func normalizeImage(data []byte) (mediaType string, out []byte, err error) {
	decodeSlot <- struct{}{}
	defer func() { <-decodeSlot }()

	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", nil, reject("This image couldn't be read.", false)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return "", nil, reject("This image couldn't be read.", false)
	}
	if cfg.Width > MaxImageSide || cfg.Height > MaxImageSide {
		return "", nil, reject("This image is too large. Use one up to 4096 pixels on each side.", false)
	}
	heavy, herr := isHeavy(data)
	if herr != nil {
		return "", nil, reject("This image couldn't be read.", false)
	}
	if px := cfg.Width * cfg.Height; px > maxPixels || (px > maxHeavyPixels && heavy) {
		return "", nil, reject("This image is too large. Use a smaller one.", false)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", nil, reject("This image couldn't be read.", false)
	}
	var buf bytes.Buffer
	switch format {
	case "png", "gif":
		if err := png.Encode(&buf, img); err != nil {
			return "", nil, reject("This image couldn't be read.", false)
		}
		return "image/png", buf.Bytes(), nil
	case "jpeg", "webp":
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return "", nil, reject("This image couldn't be read.", false)
		}
		return "image/jpeg", buf.Bytes(), nil
	}
	return "", nil, reject("This image couldn't be read.", false)
}
