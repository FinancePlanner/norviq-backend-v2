package articles

import (
	"bytes"
	"net/http"

	"norviq-api/internal/httpx"
)

// sniffedImage is SniffedImage.
type sniffedImage struct {
	ContentType string
	Width       int
	Height      int
}

const (
	maxImageBytes = 2_000_000
	maxImageSide  = 4096
)

// sniffImage is ArticleImageSniffer.sniff: identifies a cover by its bytes,
// never by the client's claimed type, so an SVG or HTML file can't be stored
// and served back as an "image".
func sniffImage(b []byte) (sniffedImage, error) {
	if len(b) > maxImageBytes {
		return sniffedImage{}, httpx.Abort(http.StatusRequestEntityTooLarge, "Images must be 2 MB or smaller.")
	}
	var img *sniffedImage
	switch {
	case bytes.HasPrefix(b, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		img = sniffPNG(b)
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		img = sniffJPEG(b)
	case len(b) >= 12 && bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		img = sniffWebP(b)
	default:
		return sniffedImage{}, httpx.Abort(http.StatusUnsupportedMediaType, "Use a JPEG, PNG or WebP image.")
	}
	if img == nil || img.Width <= 0 || img.Height <= 0 {
		return sniffedImage{}, httpx.BadRequest("That image file looks damaged.")
	}
	if img.Width > maxImageSide || img.Height > maxImageSide {
		return sniffedImage{}, httpx.BadRequest("Images can be at most 4096 pixels on a side.")
	}
	return *img, nil
}

func be16(b []byte, i int) int { return int(b[i])<<8 | int(b[i+1]) }
func le16(b []byte, i int) int { return int(b[i]) | int(b[i+1])<<8 }
func be32(b []byte, i int) int {
	return int(b[i])<<24 | int(b[i+1])<<16 | int(b[i+2])<<8 | int(b[i+3])
}

func sniffPNG(b []byte) *sniffedImage {
	if len(b) < 24 {
		return nil
	}
	return &sniffedImage{"image/png", be32(b, 16), be32(b, 20)}
}

// sniffJPEG walks the marker segments to the first start-of-frame.
func sniffJPEG(b []byte) *sniffedImage {
	i := 2
	for i+9 < len(b) {
		if b[i] != 0xFF {
			return nil
		}
		// Any marker may be preceded by 0xFF fill bytes.
		if b[i+1] == 0xFF {
			i++
			continue
		}
		marker := b[i+1]
		length := be16(b, i+2)
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			return &sniffedImage{"image/jpeg", be16(b, i+7), be16(b, i+5)}
		}
		if length < 2 {
			return nil
		}
		i += 2 + length
	}
	return nil
}

func sniffWebP(b []byte) *sniffedImage {
	if len(b) < 30 {
		return nil
	}
	switch string(b[12:16]) {
	case "VP8 ":
		return &sniffedImage{"image/webp", le16(b, 26) & 0x3FFF, le16(b, 28) & 0x3FFF}
	case "VP8L":
		bits := int(b[21]) | int(b[22])<<8 | int(b[23])<<16 | int(b[24])<<24
		return &sniffedImage{"image/webp", (bits & 0x3FFF) + 1, ((bits >> 14) & 0x3FFF) + 1}
	case "VP8X":
		w := (int(b[24]) | int(b[25])<<8 | int(b[26])<<16) + 1
		h := (int(b[27]) | int(b[28])<<8 | int(b[29])<<16) + 1
		return &sniffedImage{"image/webp", w, h}
	}
	return nil
}
