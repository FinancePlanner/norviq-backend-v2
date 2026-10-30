package articles

import (
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/articlesdb"

	"github.com/labstack/echo/v5"
)

// visibilityRequest is ArticleVisibilityRequest.
type visibilityRequest struct {
	Hidden bool `json:"hidden"`
}

// setVisibility is ArticlesController.setVisibility
// (PUT /v1/admin/articles/:ref/visibility): an admin hides or republishes
// an article. A deleted article stays deleted (404).
func (h *handler) setVisibility(c *echo.Context) error {
	viewer, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if !viewer.IsAdmin {
		return httpx.Forbidden("Admin access required.")
	}
	ctx := c.Request().Context()
	a, err := h.find(ctx, c.Param("ref"))
	if err != nil {
		return err
	}
	if a == nil || a.Status == "deleted" {
		return notFoundArticle()
	}
	var body visibilityRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	status := "published"
	if body.Hidden {
		status = "hidden"
	}
	// Fluent's save after an assignment always writes, updated_at included.
	now := h.now()
	if err := h.q.SetArticleStatus(ctx, articlesdb.SetArticleStatusParams{Status: status, UpdatedAt: &now, ID: a.ID}); err != nil {
		return err
	}
	h.d.Logger.Info("articles.visibility", "code", a.Code, "hidden", body.Hidden, "by", viewer.UserID)
	return httpx.NoContent(c)
}
