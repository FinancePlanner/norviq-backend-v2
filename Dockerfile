# syntax=docker/dockerfile:1.7
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY db ./db
ARG GIT_SHA=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.gitSHA=${GIT_SHA}" -o /out/norviq-api ./cmd/api

# distroless/static carries CA roots and tzdata, which outbound TLS and the
# Lisbon-time schedules need, and nothing else.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/norviq-api /app/norviq-api
WORKDIR /app
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/norviq-api"]
CMD ["serve"]
