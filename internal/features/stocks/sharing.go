package stocks

import (
	"net/http"
	"os"
	"strings"

	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// sharingConfig is SharingController's environment.
type sharingConfig struct {
	appStoreURL   string // SHARE_APP_STORE_URL
	publicBaseURL string // SHARE_PUBLIC_BASE_URL
	ogImageURL    string // SHARE_OG_IMAGE_URL
}

const appStoreFallback = "https://apps.apple.com/us/app/norviqa/id6745227236"

func loadSharingConfig() sharingConfig {
	c := sharingConfig{
		publicBaseURL: os.Getenv("SHARE_PUBLIC_BASE_URL"),
		ogImageURL:    os.Getenv("SHARE_OG_IMAGE_URL"),
	}
	// Environment.get(...) ?? fallback: an empty value is kept.
	if v, ok := os.LookupEnv("SHARE_APP_STORE_URL"); ok {
		c.appStoreURL = v
	} else {
		c.appStoreURL = appStoreFallback
	}
	return c
}

func (s sharingConfig) shareURL(c *echo.Context, path string) string {
	if s.publicBaseURL != "" {
		return strings.TrimSuffix(s.publicBaseURL, "/") + path
	}
	scheme := c.Request().Header.Get("X-Forwarded-Proto")
	if _, ok := c.Request().Header["X-Forwarded-Proto"]; !ok {
		scheme = "https"
	}
	host := c.Request().Host
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host + path
}

// defaultOGImageURL is the site-wide social card norviq-web-v2 serves. The
// API never shipped the /share/static/og-default.png it used to point at.
const defaultOGImageURL = "https://norviq.org/static/images/og.png"

func (s sharingConfig) ogImage() string {
	if s.ogImageURL != "" {
		return s.ogImageURL
	}
	return defaultOGImageURL
}

// sanitizeShareSymbol keeps the leading run of alphanumerics, "." and "-",
// uppercased.
func sanitizeShareSymbol(raw string) string {
	end := len(raw)
	for i, r := range raw {
		if !isSwiftAlphanumeric(r) && r != '.' && r != '-' {
			end = i
			break
		}
	}
	return swiftUpper(raw[:end])
}

// GET /share/stock/:symbol
func (h *handlers) shareStock(c *echo.Context) error {
	symbol := sanitizeShareSymbol(pathParam(c, "symbol"))
	if symbol == "" {
		return httpx.BadRequest("Symbol is required.")
	}
	html := renderSharePage(sharePage{
		ogType:      "website",
		title:       "$" + symbol + " on Norviqa",
		description: "Track " + symbol + " — fundamentals, valuation, and your thesis on Norviqa.",
		url:         h.sharing.shareURL(c, "/share/stock/"+symbol),
		imageURL:    h.sharing.ogImage(),
		twitterCard: "summary_large_image",
		redirect:    h.sharing.appStoreURL,
		extraHead:   "<meta name=\"al:ios:app_store_id\" content=\"6745227236\">\n<meta name=\"al:ios:app_name\" content=\"Norviqa\">",
	})
	c.Response().Header().Set("Cache-Control", "public, max-age=300")
	return c.Blob(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

type sharePage struct {
	ogType, title, description, url, imageURL, twitterCard, redirect, extraHead string
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

// renderSharePage is SharingHTMLRenderer.render.
func renderSharePage(p sharePage) string {
	e := htmlEscaper.Replace
	title, desc, u, img, redirect := e(p.title), e(p.description), e(p.url), e(p.imageURL), e(p.redirect)
	extra := ""
	if p.extraHead != "" {
		extra = "\n    " + p.extraHead
	}
	return `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>` + title + `</title>
    <meta name="description" content="` + desc + `">
    <meta property="og:type" content="` + e(p.ogType) + `">
    <meta property="og:title" content="` + title + `">
    <meta property="og:description" content="` + desc + `">
    <meta property="og:url" content="` + u + `">
    <meta property="og:image" content="` + img + `">
    <meta property="og:site_name" content="Norviqa">
    <meta name="twitter:card" content="` + e(p.twitterCard) + `">
    <meta name="twitter:title" content="` + title + `">
    <meta name="twitter:description" content="` + desc + `">
    <meta name="twitter:image" content="` + img + `">` + extra + `
    <meta http-equiv="refresh" content="0; url=` + redirect + `">
</head>
<body>
    <p>Opening Norviqa… <a href="` + redirect + `">Continue to the app</a>.</p>
</body>
</html>`
}
