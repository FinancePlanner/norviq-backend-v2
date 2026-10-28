package httpx

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
)

// The helpers below port Vapor's `req.query[T.self, at: "x"]` subscript,
// which is `try? req.query.get(...)`: a missing OR malformed value is nil,
// never a 400 (contrast QueryInt, which ports the throwing `get`). Vapor's
// URL-encoded form decoder kept the last of repeated keys, and read a bare
// key with no "=" as a boolean flag.

// queryValue returns the last value of name and whether name appeared with a
// value, plus whether it appeared as a bare flag.
func queryValue(c *echo.Context, name string) (value string, hasValue, isFlag bool) {
	for _, part := range strings.Split(c.Request().URL.RawQuery, "&") {
		if part == "" {
			continue
		}
		rawKey, rawVal, hasEq := strings.Cut(part, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil || key != name {
			continue
		}
		if !hasEq {
			isFlag = true
			continue
		}
		v, err := url.QueryUnescape(rawVal)
		if err != nil {
			continue
		}
		value, hasValue = v, true
	}
	return value, hasValue, isFlag
}

// QueryString is `req.query[String.self, at: name]`: nil when absent or a
// bare flag; "" when present with an empty value.
func QueryString(c *echo.Context, name string) *string {
	v, ok, _ := queryValue(c, name)
	if !ok {
		return nil
	}
	return &v
}

// QueryOptionalInt is `req.query[Int.self, at: name]`: nil when absent or
// not an integer.
func QueryOptionalInt(c *echo.Context, name string) *int {
	v, ok, _ := queryValue(c, name)
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

// QueryOptionalBool is `req.query[Bool.self, at: name]`: "1"/"true"/"on"
// and "0"/"false" (any case), a bare `?name` flag is true, anything else nil.
func QueryOptionalBool(c *echo.Context, name string) *bool {
	v, ok, flag := queryValue(c, name)
	t, f := true, false
	if !ok {
		if flag {
			return &t
		}
		return nil
	}
	switch strings.ToLower(v) {
	case "1", "true", "on":
		return &t
	case "0", "false":
		return &f
	}
	return nil
}

// IntOr dereferences p, or returns def.
func IntOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}
