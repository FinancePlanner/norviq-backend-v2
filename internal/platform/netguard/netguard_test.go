package netguard

import (
	"errors"
	"testing"

	"norviq-api/internal/httpx"
)

func TestValidate(t *testing.T) {
	for raw, want := range map[string]string{
		"http://api.example.com/v1":      "URL must use https.",
		"https://localhost/v1":           "Host localhost is not allowed.",
		"https://169.254.169.254/latest": "Host 169.254.169.254 is not allowed.",
		"https://10.0.0.1/v1":            "Host 10.0.0.1 is not allowed.",
		"https://172.20.1.1":             "Host 172.20.1.1 is not allowed.",
		"https://192.168.1.10":           "Host 192.168.1.10 is not allowed.",
		"https://printer.local":          "Host printer.local is not allowed.",
		"https://[::1]/v1":               "Host ::1 is not allowed.",
		"https://127.8.8.8":              "Host 127.8.8.8 is not allowed.",
		"not a url":                      "URL is not a valid absolute URL.",
		"/relative":                      "URL is not a valid absolute URL.",
	} {
		_, err := Validate(raw)
		if err == nil || err.Error() != want {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, ok := range []string{"https://api.example.com/v1", "https://172.32.0.1", "https://172.15.0.1", " https://llm.example "} {
		if _, err := Validate(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
}

func TestValidateForRequestIs400(t *testing.T) {
	_, err := ValidateForRequest("http://x.example")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 400 || he.Reason != "URL must use https." {
		t.Fatal(err)
	}
}
