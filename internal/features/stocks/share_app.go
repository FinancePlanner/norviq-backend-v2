package stocks

import (
	"net/http"

	"github.com/labstack/echo/v5"
)

// GET /share/app — SharingController.appLanding.
func (h *handlers) shareApp(c *echo.Context) error {
	html := renderSharePage(sharePage{
		ogType:      "website",
		title:       "Norviqa — your portfolio companion",
		description: "Plan, track, and understand your investments. Available on iOS.",
		url:         h.sharing.shareURL(c, "/share/app"),
		imageURL:    h.sharing.ogImage(),
		twitterCard: "summary_large_image",
		redirect:    h.sharing.appStoreURL,
	})
	c.Response().Header().Set("Cache-Control", "public, max-age=300")
	return c.Blob(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}
