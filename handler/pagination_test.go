package handler_test

import (
	"net/http/httptest"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

func TestParsePagination_Defaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/videos", nil)
	p := handler.ParsePagination(r)

	if p.Limit != handler.DefaultLimit {
		t.Errorf("limit: got %d, want %d", p.Limit, handler.DefaultLimit)
	}
	if p.Offset != 0 {
		t.Errorf("offset: got %d, want 0", p.Offset)
	}
}

func TestParsePagination_CustomValues(t *testing.T) {
	r := httptest.NewRequest("GET", "/videos?limit=10&offset=30", nil)
	p := handler.ParsePagination(r)

	if p.Limit != 10 {
		t.Errorf("limit: got %d, want 10", p.Limit)
	}
	if p.Offset != 30 {
		t.Errorf("offset: got %d, want 30", p.Offset)
	}
}

func TestParsePagination_ClampsMaxLimit(t *testing.T) {
	r := httptest.NewRequest("GET", "/videos?limit=500", nil)
	p := handler.ParsePagination(r)

	if p.Limit != handler.MaxLimit {
		t.Errorf("limit: got %d, want %d", p.Limit, handler.MaxLimit)
	}
}

func TestParsePagination_IgnoresInvalidValues(t *testing.T) {
	r := httptest.NewRequest("GET", "/videos?limit=abc&offset=-5", nil)
	p := handler.ParsePagination(r)

	if p.Limit != handler.DefaultLimit {
		t.Errorf("limit: got %d, want %d (default)", p.Limit, handler.DefaultLimit)
	}
	if p.Offset != 0 {
		t.Errorf("offset: got %d, want 0 (default)", p.Offset)
	}
}

func TestParsePagination_ZeroLimit_UsesDefault(t *testing.T) {
	r := httptest.NewRequest("GET", "/videos?limit=0", nil)
	p := handler.ParsePagination(r)

	if p.Limit != handler.DefaultLimit {
		t.Errorf("limit: got %d, want %d (default for 0)", p.Limit, handler.DefaultLimit)
	}
}

func TestNewPaginatedResponse(t *testing.T) {
	data := []string{"a", "b", "c"}
	p := handler.Pagination{Limit: 3, Offset: 0}

	resp := handler.NewPaginatedResponse(data, 10, p)

	if len(resp.Data) != 3 {
		t.Errorf("data length: got %d, want 3", len(resp.Data))
	}
	if resp.Total != 10 {
		t.Errorf("total: got %d, want 10", resp.Total)
	}
	if resp.Limit != 3 {
		t.Errorf("limit: got %d, want 3", resp.Limit)
	}
	if resp.Offset != 0 {
		t.Errorf("offset: got %d, want 0", resp.Offset)
	}
	if !resp.HasMore {
		t.Error("has_more: expected true (3 < 10)")
	}
}

func TestNewPaginatedResponse_NoMore(t *testing.T) {
	data := []string{"a", "b"}
	p := handler.Pagination{Limit: 10, Offset: 8}

	resp := handler.NewPaginatedResponse(data, 10, p)

	if resp.HasMore {
		t.Error("has_more: expected false (8+2 >= 10)")
	}
}

func TestNewPaginatedResponse_NilData(t *testing.T) {
	p := handler.Pagination{Limit: 10, Offset: 0}

	resp := handler.NewPaginatedResponse[string](nil, 0, p)

	if resp.Data == nil {
		t.Error("data should be empty slice, not nil")
	}
	if len(resp.Data) != 0 {
		t.Errorf("data length: got %d, want 0", len(resp.Data))
	}
}
