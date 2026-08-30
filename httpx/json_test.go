package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnmarshalStrict(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name string `json:"name"`
	}

	t.Run("one declared value", func(t *testing.T) {
		t.Parallel()
		var got payload
		if err := UnmarshalStrict([]byte(" \n\t{\"name\":\"Ada\"}\n"), &got); err != nil {
			t.Fatalf("UnmarshalStrict: %v", err)
		}
		if got.Name != "Ada" {
			t.Fatalf("name = %q, want Ada", got.Name)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		t.Parallel()
		var got payload
		err := UnmarshalStrict([]byte(`{"name":"Ada","admin":true}`), &got)
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("error = %v, want unknown-field rejection", err)
		}
	})

	t.Run("second value", func(t *testing.T) {
		t.Parallel()
		var got payload
		if err := UnmarshalStrict([]byte(`{"name":"Ada"} {"name":"Grace"}`), &got); err == nil {
			t.Fatal("second JSON value was accepted")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		var got payload
		if err := UnmarshalStrict(nil, &got); err == nil {
			t.Fatal("empty input was accepted")
		}
	})
}

func TestDecodeStrictJSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name string `json:"name"`
	}

	t.Run("one bounded declared value", func(t *testing.T) {
		t.Parallel()
		body := `{"name":"Ada"}`
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var got payload
		if err := DecodeStrictJSON(httptest.NewRecorder(), request, int64(len(body)), &got); err != nil {
			t.Fatalf("DecodeStrictJSON: %v", err)
		}
		if got.Name != "Ada" {
			t.Fatalf("name = %q, want Ada", got.Name)
		}
	})

	t.Run("unknown and trailing values", func(t *testing.T) {
		t.Parallel()
		for _, body := range []string{
			`{"name":"Ada","admin":true}`,
			`{"name":"Ada"} {"name":"Grace"}`,
		} {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			var got payload
			if err := DecodeStrictJSON(httptest.NewRecorder(), request, 1024, &got); err == nil {
				t.Errorf("DecodeStrictJSON accepted %q", body)
			}
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		t.Parallel()
		body := `{"name":"Ada"}`
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var got payload
		err := DecodeStrictJSON(httptest.NewRecorder(), request, int64(len(body)-1), &got)
		var tooLarge *http.MaxBytesError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("error = %v, want *http.MaxBytesError", err)
		}
	})

	t.Run("oversized trailing whitespace", func(t *testing.T) {
		t.Parallel()
		body := `{"name":"Ada"}`
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body+"  "))
		var got payload
		err := DecodeStrictJSON(httptest.NewRecorder(), request, int64(len(body)+1), &got)
		var tooLarge *http.MaxBytesError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("error = %v, want *http.MaxBytesError", err)
		}
	})

	t.Run("invalid limit", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Fatal("DecodeStrictJSON did not panic")
			}
		}()
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
		_ = DecodeStrictJSON(httptest.NewRecorder(), request, 0, &payload{})
	})
}
