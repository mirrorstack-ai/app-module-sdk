package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// UnmarshalStrict decodes exactly one JSON value into target. Object fields
// not declared by a struct target and any second/trailing JSON value are
// rejected.
//
// Use this for module-owned contracts whose shape must not silently drift,
// such as contribution payloads and bounded inter-module responses. It is
// intentionally opt-in; ordinary encoding/json compatibility remains useful
// for forward-compatible platform envelopes.
func UnmarshalStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	return decodeStrict(decoder, target)
}

// DecodeStrictJSON decodes one size-bounded HTTP request body into target.
// Unknown object fields and every second or trailing JSON value are rejected.
// maxBytes must be positive; an oversized body returns *http.MaxBytesError.
//
// The caller still owns endpoint-specific validation and error mapping. This
// helper owns only the repeated transport boundary.
func DecodeStrictJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, target any) error {
	if maxBytes <= 0 {
		panic("httpx: strict JSON body limit must be positive")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	return decodeStrict(decoder, target)
}

func decodeStrict(decoder *json.Decoder, target any) error {
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return errors.New("httpx: JSON input must contain exactly one value")
}
