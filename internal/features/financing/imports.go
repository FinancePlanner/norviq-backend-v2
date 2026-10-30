package financing

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/netguard"

	"github.com/labstack/echo/v5"
)

// importClient is Vapor's app.http.client as configure.swift set it up:
// 10s connect timeout, no read timeout, AsyncHTTPClient's default redirect
// policy (follow up to 5, no cycles) and no response decompression.
//
// Vapor checked only the host of the URL the user sent, so a public page
// that redirected to (or a name that resolved to) a private address was
// still fetched (SSRF). v2 also re-checks every redirect target and refuses
// to dial a non-public IP, whatever name led to it. No proxy is used, so the
// dialed address is the real target.
func importClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: refusePrivateDial}).DialContext
	transport.DisableCompression = true
	return &http.Client{Transport: transport, CheckRedirect: checkImportRedirect}
}

// errBlockedTarget is a redirect or dial to a host an import may not reach.
var errBlockedTarget = errors.New("blocked import target")

func checkImportRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 5 {
		return errors.New("redirectLimitReached")
	}
	for _, prev := range via {
		if prev.URL.String() == req.URL.String() {
			return errors.New("redirectCycleDetected")
		}
	}
	if host, ok := importHost(req.URL.String()); !ok || host == "" {
		return errBlockedTarget
	}
	return nil
}

// refusePrivateDial is the dialer's Control hook: it sees the resolved
// address, so DNS answers pointing inside the network are refused too.
func refusePrivateDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !publicIP(ip) {
		return errBlockedTarget
	}
	return nil
}

// cgnat is 100.64.0.0/10 (carrier-grade NAT, also used inside clusters).
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// publicIP is false for loopback, private (RFC 1918 and fc00::/7),
// link-local, multicast, unspecified and CGNAT addresses.
func publicIP(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsInterfaceLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified() && !cgnat.Contains(ip)
}

// importHost is the URLComponents(string:) part of importURL: the
// lowercased host, or ok=false when the URL is not an https URL to a public
// host. Vapor kept an IPv6 literal's brackets, so "[::1]" slipped past the
// blocked-host check; v2 strips them, and also refuses non-public IP
// literals the name list misses (IPv6 private ranges, CGNAT).
func importHost(raw string) (host string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" {
		return "", false
	}
	if !strings.HasPrefix(raw[len(u.Scheme)+1:], "//") {
		return "", false // no authority: host is nil
	}
	host = strings.ToLower(u.Hostname())
	if netguard.IsBlockedHost(host) {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return "", false
	}
	return host, true
}

func (h *handlers) importURL(c *echo.Context) error {
	uid, err := sessionUser(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, uid); err != nil {
		return err
	}
	var req URLImportRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	host, ok := importHost(req.URL)
	if !ok || host == "" {
		// An empty host ("https:///x") was a 500 HTTPClientError.emptyHost
		// in Vapor; it is the same bad input as any other non-public URL.
		return errNotPublic
	}
	out, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, req.URL, nil)
	if err != nil {
		return err
	}
	out.Header.Set("User-Agent", "Norviq Financing Import/1.0")
	resp, err := h.client.Do(out)
	if errors.Is(err, errBlockedTarget) {
		return errNotPublic
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImportBytes+1))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || len(body) == 0 || len(body) > maxImportBytes {
		return httpx.BadRequest("The offer page could not be imported.")
	}
	return httpx.OK(c, extractOffer(string(body), &host))
}

var errNotPublic = httpx.BadRequest("Only public HTTPS offer pages can be imported.")

var errMissingFile = httpx.BadRequest("Missing or oversized file.")

func (h *handlers) importFile(c *echo.Context) error {
	uid, err := sessionUser(c)
	if err != nil {
		return err
	}
	// body: .collect(maxSize: "8mb") runs before the handler.
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxImportBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxImportBytes {
		return httpx.Abort(http.StatusRequestEntityTooLarge, "Payload Too Large")
	}
	if err := h.requirePro(c, uid); err != nil {
		return err
	}
	data, err := uploadedFile(c, body)
	if err != nil {
		return err
	}
	if data == nil {
		return errMissingFile
	}
	if len(data) == 0 {
		return httpx.OK(c, ImportResponse{
			Recognized: false,
			Warnings:   []string{"This file has no extractable text. Use a text-based PDF or enter the terms manually."},
		})
	}
	return httpx.OK(c, extractOffer(string(data), nil))
}

// uploadedFile decodes `struct ImportUpload: Content { var file: File? }`
// with the decoder Vapor picked for the Content-Type. nil means no file.
func uploadedFile(c *echo.Context, body []byte) ([]byte, error) {
	kind, params, err := mediaType(c)
	if err != nil {
		return nil, err
	}
	switch kind {
	case mediaMultipart:
		boundary := params["boundary"]
		if boundary == "" {
			return nil, httpx.Abort(http.StatusUnsupportedMediaType, "Unsupported Media Type")
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return nil, httpx.Abort(http.StatusUnprocessableEntity, "")
		}
		return multipartFile(body, boundary)
	case mediaForm:
		if len(bytes.TrimSpace(body)) == 0 {
			return nil, httpx.Abort(http.StatusUnprocessableEntity, "")
		}
		values, perr := url.ParseQuery(string(body))
		if perr != nil {
			return nil, httpx.BadRequest(perr.Error())
		}
		if _, ok := values["file"]; ok {
			return nil, httpx.BadRequest("No value found (expected type 'String') at path 'file.filename'. No String was found at 'file.filename'.")
		}
		return nil, nil
	default:
		if len(bytes.TrimSpace(body)) == 0 {
			return nil, httpx.Abort(http.StatusUnprocessableEntity, "")
		}
		return jsonFile(body)
	}
}

// multipartFile is FormDataDecoder: the last part named "file" wins, a part
// without a filename cannot become a File, a part without a name fails the
// whole decode, and a body MultipartKit cannot parse leaves file nil.
func multipartFile(body []byte, boundary string) ([]byte, error) {
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	type part struct {
		data        []byte
		hasFilename bool
	}
	var file *part
	for {
		p, err := r.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil //nolint:nilerr // an unparseable body decodes as no file
		}
		data, err := io.ReadAll(p)
		if err != nil {
			return nil, nil //nolint:nilerr // an unparseable body decodes as no file
		}
		_, dparams, derr := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
		name, hasName := dparams["name"]
		if derr != nil || !hasName {
			return nil, httpx.BadRequest("Value was not of type 'MultipartPart' at path ''. Expected dictionary but encountered single value.")
		}
		if name != "file" {
			continue
		}
		_, hasFilename := dparams["filename"]
		file = &part{data: data, hasFilename: hasFilename}
	}
	if file == nil {
		return nil, nil
	}
	if !file.hasFilename {
		return nil, httpx.BadRequest(`Data corrupted at path 'file'. Could not convert value at "file" to type File from multipart part.`)
	}
	if file.data == nil {
		return []byte{}, nil
	}
	return file.data, nil
}

// jsonFile decodes {"file": {"data": "<base64>", "filename": "..."}}, File's
// Codable form.
func jsonFile(body []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	raw, ok := root["file"]
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, httpx.BadRequest("Value was not of type 'Dictionary<String, Any>' at path 'file'. Expected to decode Dictionary<String, Any> but found a string instead.")
	}
	for _, key := range []string{"data", "filename"} {
		if _, ok := obj[key]; !ok {
			return nil, httpx.BadRequest(fmt.Sprintf(
				"No such key '%s' at path 'file'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).", key, key, key))
		}
	}
	var encoded, filename string
	if json.Unmarshal(obj["filename"], &filename) != nil {
		return nil, httpx.BadRequest("Value was not of type 'String' at path 'file.filename'.")
	}
	if json.Unmarshal(obj["data"], &encoded) != nil {
		return nil, httpx.BadRequest("Value was not of type 'String' at path 'file.data'.")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, httpx.BadRequest("Data corrupted at path 'file.data'. Encountered Data is not valid Base64.")
	}
	return data, nil
}
