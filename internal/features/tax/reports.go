package tax

import "github.com/labstack/echo/v5"

// reportStorage is TaxReportStorage.
type reportStorage interface{}

func reportStorageFromEnv() reportStorage { return nil }

func (h *handlers) registerReportReads(_ *echo.Group, _ []echo.MiddlewareFunc) {}

func (h *handlers) registerReportWrites(_ *echo.Group, _ []echo.MiddlewareFunc) {}
