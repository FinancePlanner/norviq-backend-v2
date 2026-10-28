package httpx

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"norviq-api/internal/apijson"

	"github.com/labstack/echo/v5"
)

// Serializer plugs apijson into echo so c.JSON and c.Bind speak the Vapor
// wire format.
type Serializer struct{}

func (Serializer) Serialize(c *echo.Context, target any, _ string) error {
	body, err := apijson.Marshal(target)
	if err != nil {
		return err
	}
	_, err = c.Response().Write(body)
	return err
}

func (Serializer) Deserialize(c *echo.Context, target any) error {
	if err := apijson.Decode(c.Request().Body, target); err != nil {
		if errors.Is(err, io.EOF) {
			return BadRequest("Request body is empty.")
		}
		return BadRequest(err.Error())
	}
	return nil
}

// JSON writes v with Vapor's content type. Vapor sent
// "application/json; charset=utf-8" for every Content response.
func JSON(c *echo.Context, status int, v any) error {
	body, err := apijson.Marshal(v)
	if err != nil {
		return err
	}
	return c.Blob(status, "application/json; charset=utf-8", body)
}

// OK is JSON with 200.
func OK(c *echo.Context, v any) error { return JSON(c, http.StatusOK, v) }

// Created is JSON with 201.
func Created(c *echo.Context, v any) error { return JSON(c, http.StatusCreated, v) }

// NoContent answers 204, which Vapor returned for `HTTPStatus` handlers.
func NoContent(c *echo.Context) error { return c.NoContent(http.StatusNoContent) }

// Decode reads the JSON body into v (snake_case or camelCase keys).
func Decode(c *echo.Context, v any) error {
	if err := apijson.Decode(c.Request().Body, v); err != nil {
		if errors.Is(err, io.EOF) {
			return BadRequest("Request body is empty.")
		}
		return BadRequest(err.Error())
	}
	return nil
}

// QueryInt reads an optional integer query parameter. A malformed value is a
// 400, as Vapor's `req.query.get(Int.self, at:)` threw.
func QueryInt(c *echo.Context, name string) (*int, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil, BadRequest("Value for key '" + name + "' is not an Int.")
	}
	return &v, nil
}
