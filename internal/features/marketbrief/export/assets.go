// Package export renders a market brief as PNG and PDF. It must not import the marketbrief
// package: marketbrief will call into it, and the reverse import would be a cycle.
package export

import (
	"bytes"
	"embed"
	"image"
	_ "image/png" // registers the PNG decoder for LogoDark
)

//go:embed assets/fonts/*.ttf assets/logo-symbol-dark.png
var assetFS embed.FS

// Font names one of the embedded typefaces. Serif is Brygada 1918 (headlines), sans is Geist (body and data).
type Font int

const (
	SerifRegular Font = iota
	SerifSemiBold
	SansRegular
	SansMedium
	SansSemiBold
)

var fontFiles = map[Font]string{
	SerifRegular:  "assets/fonts/Brygada1918-Regular.ttf",
	SerifSemiBold: "assets/fonts/Brygada1918-SemiBold.ttf",
	SansRegular:   "assets/fonts/Geist-Regular.ttf",
	SansMedium:    "assets/fonts/Geist-Medium.ttf",
	SansSemiBold:  "assets/fonts/Geist-SemiBold.ttf",
}

// FontBytes returns the TTF bytes for f. The files are embedded, so an unknown Font is a programming error.
func FontBytes(f Font) []byte {
	name, ok := fontFiles[f]
	if !ok {
		panic("export: unknown font")
	}
	b, err := assetFS.ReadFile(name)
	if err != nil {
		panic("export: embedded font missing: " + err.Error())
	}
	return b
}

// LogoDark is the brand symbol for dark backgrounds. It decodes once at init and panics on failure
// because the asset is embedded and a bad build should not reach production.
var LogoDark = mustLogo()

func mustLogo() image.Image {
	b, err := assetFS.ReadFile("assets/logo-symbol-dark.png")
	if err != nil {
		panic("export: embedded logo missing: " + err.Error())
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		panic("export: embedded logo undecodable: " + err.Error())
	}
	return img
}
