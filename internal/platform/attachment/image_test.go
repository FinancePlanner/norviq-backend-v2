package attachment

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"testing"

	"norviq-api/internal/platform/multipartform"
)

const (
	reasonImagesOnly = "Only JPEG, PNG, WebP and GIF images are supported."
	reasonDocsOnly   = "Only PDF and CSV files are supported."
	reasonBoth       = "Only images, PDFs and CSV files are supported."
)

var allowAll = Allow{Images: true, Documents: true}

func part(name, ct string, body []byte) multipartform.Part {
	n := name
	return multipartform.Part{Name: "files", Filename: &n, ContentType: ct, Body: body}
}

func testImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 20), uint8(y * 20), 90, 255})
		}
	}
	return img
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, testImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, testImage(w, h), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func wantRejection(t *testing.T, err error, status int, reason string) {
	t.Helper()
	_ = wantRejectionDoc(t, err, status, reason)
}

// wantRejectionDoc is wantRejection that also returns the rejection.
func wantRejectionDoc(t *testing.T, err error, status int, reason string) *Rejection {
	t.Helper()
	var rej *Rejection
	if !errors.As(err, &rej) {
		t.Fatalf("want *Rejection, got %v", err)
	}
	if rej.Status != status || rej.Reason != reason {
		t.Fatalf("got %d %q, want %d %q", rej.Status, rej.Reason, status, reason)
	}
	return rej
}

func TestValidateReencodesPNGAndDropsTrailingPayload(t *testing.T) {
	src := pngBytes(t, 8, 6)
	poisoned := append(append([]byte{}, src...), []byte("PK\x03\x04 evil zip")...)
	out, err := Validate([]multipartform.Part{part("a.png", "image/png", poisoned)}, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Kind != KindImage || out[0].MediaType != "image/png" {
		t.Fatalf("got %+v", out)
	}
	if bytes.Contains(out[0].Data, []byte("PK\x03\x04")) {
		t.Fatal("trailing payload survived")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out[0].Data))
	if err != nil || cfg.Width != 8 || cfg.Height != 6 {
		t.Fatalf("cfg %+v err %v", cfg, err)
	}
}

func TestValidateStripsJPEGExif(t *testing.T) {
	src := jpegBytes(t, 8, 8)
	exif := append([]byte("Exif\x00\x00"), []byte("GPS-secret")...)
	seg := []byte{0xFF, 0xE1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}
	seg = append(seg, exif...)
	withExif := append(append(append([]byte{}, src[:2]...), seg...), src[2:]...)
	if !bytes.Contains(withExif, []byte("Exif\x00\x00")) {
		t.Fatal("fixture lacks exif")
	}
	out, err := Validate([]multipartform.Part{part("a.jpg", "image/jpeg", withExif)}, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].MediaType != "image/jpeg" || bytes.Contains(out[0].Data, []byte("Exif\x00\x00")) || bytes.Contains(out[0].Data, []byte("GPS-secret")) {
		t.Fatalf("exif survived or wrong type %q", out[0].MediaType)
	}
}

// 1x1 lossless WebP.
const tinyWebP = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="

func TestValidateWebPBecomesJPEG(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(tinyWebP)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Validate([]multipartform.Part{part("a.webp", "image/webp", data)}, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].MediaType != "image/jpeg" || !bytes.HasPrefix(out[0].Data, []byte{0xFF, 0xD8}) {
		t.Fatalf("got %q", out[0].MediaType)
	}
}

func TestValidateGIFBecomesSingleFramePNG(t *testing.T) {
	pal := color.Palette{color.Black, color.White}
	f1 := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	f2 := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{f1, f2}, Delay: []int{1, 1}}); err != nil {
		t.Fatal(err)
	}
	out, err := Validate([]multipartform.Part{part("a.gif", "image/gif", b.Bytes())}, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].MediaType != "image/png" {
		t.Fatalf("got %q", out[0].MediaType)
	}
	if _, err := png.Decode(bytes.NewReader(out[0].Data)); err != nil {
		t.Fatal(err)
	}
}

func chunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
	return b.Bytes()
}

func TestValidateRefusesHugeDimensionsBeforeDecode(t *testing.T) {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 20000)
	binary.BigEndian.PutUint32(ihdr[4:], 20000)
	ihdr[8], ihdr[9] = 8, 2
	data := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", ihdr)...)
	data = append(data, chunk("IDAT", []byte{0x78, 0x9c, 0x01})...) // truncated
	_, err := Validate([]multipartform.Part{part("a.png", "image/png", data)}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "This image is too large. Use one up to 4096 pixels on each side.")
}

func TestValidateTruncatedImageUnreadable(t *testing.T) {
	src := pngBytes(t, 16, 16)
	_, err := Validate([]multipartform.Part{part("a.png", "image/png", src[:len(src)-30])}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "This image couldn't be read.")
}

func TestValidateTrustsBytesNotDeclaredType(t *testing.T) {
	_, err := Validate([]multipartform.Part{part("a.png", "image/png", []byte("<html><body>hi</body></html>"))}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, reasonBoth)
}

func TestValidateCountsAndSizes(t *testing.T) {
	img := pngBytes(t, 4, 4)
	four := []multipartform.Part{part("1.png", "", img), part("2.png", "", img), part("3.png", "", img), part("4.png", "", img)}
	_, err := Validate(four, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "Attach up to 3 files.")

	big := append(append([]byte{}, img...), make([]byte, MaxImageBytes)...)
	_, err = Validate([]multipartform.Part{part("big.png", "", big)}, allowAll)
	wantRejection(t, err, http.StatusRequestEntityTooLarge, "Each image must be 5 MB or smaller.")
}

func TestValidateImagesOffRefusesImage(t *testing.T) {
	rej := func() *Rejection {
		_, err := Validate([]multipartform.Part{part("a.png", "", pngBytes(t, 4, 4))}, Allow{Documents: true})
		return wantRejectionDoc(t, err, http.StatusBadRequest, reasonDocsOnly)
	}()
	if rej.Document {
		t.Fatal("an image refusal is not a Document refusal")
	}
}

func TestValidateNothingAllowedOrEmpty(t *testing.T) {
	out, err := Validate(nil, allowAll)
	if err != nil || len(out) != 0 {
		t.Fatalf("got %v %v", out, err)
	}
}

func TestValidateRefuses16BitPNGOverBudget(t *testing.T) {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 3000)
	binary.BigEndian.PutUint32(ihdr[4:], 3000)
	ihdr[8], ihdr[9] = 16, 6
	data := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", ihdr)...)
	_, err := Validate([]multipartform.Part{part("a.png", "", data)}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "This image is too large. Use a smaller one.")
	// 2048x2048 at 16 bits is exactly on budget: past the gate (then unreadable).
	binary.BigEndian.PutUint32(ihdr[0:], 2048)
	binary.BigEndian.PutUint32(ihdr[4:], 2048)
	data = append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", ihdr)...)
	_, err = Validate([]multipartform.Part{part("a.png", "", data)}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "This image couldn't be read.")
}

func progressiveJPEGHeader(w, h int) []byte {
	b := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 4, 0, 0} // SOI, tiny APP0
	b = append(b, 0xFF, 0xC2, 0, 17, 8, byte(h>>8), byte(h), byte(w>>8), byte(w), 3,
		1, 0x11, 0, 2, 0x11, 0, 3, 0x11, 0)
	return append(b, 0xFF, 0xDA, 0, 12, 3, 1, 0, 2, 0, 3, 0, 0, 63, 0) // SOS, truncated body
}

func TestValidateRefusesProgressiveJPEGOverBudget(t *testing.T) {
	_, err := Validate([]multipartform.Part{part("a.jpg", "", progressiveJPEGHeader(3000, 3000))}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "This image is too large. Use a smaller one.")
	_, err = Validate([]multipartform.Part{part("a.jpg", "", progressiveJPEGHeader(2048, 2048))}, allowAll)
	wantRejection(t, err, http.StatusBadRequest, "This image couldn't be read.")
}
