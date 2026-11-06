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
)

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
