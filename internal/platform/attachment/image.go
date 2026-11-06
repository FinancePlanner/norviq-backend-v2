package attachment

import (
	"bytes"
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

// isHeavy reports a 16-bit PNG or a progressive JPEG, read from the header.
func isHeavy(data []byte) bool {
	if len(data) > 24 && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return data[24] == 16 // IHDR bit depth
	}
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return false
	}
	for i := 2; i+3 < len(data); {
		if data[i] != 0xFF {
			return false
		}
		m := data[i+1]
		switch {
		case m == 0xFF: // fill byte
			i++
			continue
		case m == 0xC2:
			return true
		case m == 0xDA: // first SOS: no SOF2 came before it
			return false
		}
		i += 2 + int(data[i+2])<<8 + int(data[i+3])
	}
	return false
}

func isSupportedImage(sniffed string) bool {
	switch sniffed {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	}
	return false
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
	if px := cfg.Width * cfg.Height; px > maxPixels || (px > maxHeavyPixels && isHeavy(data)) {
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
