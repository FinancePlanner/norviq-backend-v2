package banking

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/store/bankingdb"

	"github.com/labstack/echo/v5"
)

// callback is BankCallbackController: the unauthenticated hosted-link
// redirect target. It completes the link and 303s back to the app URI the
// flow stored, with ?bank=connected or ?bank=error&error=<reason>.
func (h *handlers) callback(kind Kind, referenceKey string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		reference := strings.TrimFunc(deref(httpx.QueryString(c, referenceKey)), unicode.IsSpace)
		var providerError *string
		if e := httpx.QueryString(c, "error"); e != nil {
			providerError = ptr(strings.TrimFunc(*e, unicode.IsSpace))
		}
		if reference == "" {
			return httpx.BadRequest("Unknown bank link reference.")
		}
		flow, err := h.b.q.GetLinkFlow(ctx, bankingdb.GetLinkFlowParams{Reference: reference, Provider: string(kind)})
		if isNoRows(err) {
			return httpx.BadRequest("Unknown bank link reference.")
		}
		if err != nil {
			return err
		}
		base := flow.AppRedirectUri
		if providerError != nil && *providerError != "" {
			return seeOther(c, appURL(base, "error", providerError))
		}
		conn, err := func() (*bankingdb.BankConnection, error) {
			p, err := h.registry.Provider(kind)
			if err != nil {
				return nil, err
			}
			return p.CompleteHostedLink(ctx, h.call(c), reference)
		}()
		if err != nil {
			return seeOther(c, appURL(base, "error", ptr(errorReason(err))))
		}
		achievementhooks.Schedule(conn.UserID)
		return seeOther(c, appURL(base, "connected", nil))
	}
}

// errorReason is `(error as? AbortError)?.reason ?? error.localizedDescription`.
func errorReason(err error) string {
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		if apiErr.Reason == "" {
			return http.StatusText(apiErr.Status)
		}
		return apiErr.Reason
	}
	// Foundation's localizedDescription for a non-NSError Swift error.
	return fmt.Sprintf("The operation couldn’t be completed. (%s)", err.Error())
}

func seeOther(c *echo.Context, location string) error {
	c.Response().Header().Set(echo.HeaderLocation, location)
	return c.NoContent(http.StatusSeeOther)
}

// appURL is BankCallbackController.appURL: base with `bank` (and `error`)
// appended to its query, encoded as URLComponents.queryItems does.
func appURL(base, status string, message *string) string {
	rest, fragment, hasFragment := strings.Cut(base, "#")
	path, query, _ := strings.Cut(rest, "?")
	var items []string
	if query != "" {
		for _, part := range strings.Split(query, "&") {
			name, value, hasValue := strings.Cut(part, "=")
			item := percentEncode(unescape(name))
			if hasValue {
				item += "=" + percentEncode(unescape(value))
			}
			items = append(items, item)
		}
	}
	items = append(items, "bank="+percentEncode(status))
	if message != nil && *message != "" {
		items = append(items, "error="+percentEncode(*message))
	}
	out := path + "?" + strings.Join(items, "&")
	if hasFragment {
		out += "#" + fragment
	}
	return out
}

func unescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

// urlQueryAllowedItem is CharacterSet.urlQueryAllowed minus "&" and "=",
// which URLComponents.queryItems escapes so a value cannot split the item.
const urlQueryAllowedItem = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~!$'()*+,;:@/?"

// percentEncode escapes every UTF-8 byte not in allowed.
func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x80 && strings.IndexByte(urlQueryAllowedItem, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
