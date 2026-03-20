package handler

import (
	"net/http"
	"strconv"
)

const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// Pagination holds parsed pagination parameters from the request.
type Pagination struct {
	Limit  int
	Offset int
}

// ParsePagination extracts limit and offset from query parameters.
// Defaults to limit=20, offset=0. Clamps limit to [1, 100].
//
//	p := handler.ParsePagination(r)
//	rows, _ := queries.ListVideos(ctx, p.Limit, p.Offset)
func ParsePagination(r *http.Request) Pagination {
	p := Pagination{
		Limit:  DefaultLimit,
		Offset: 0,
	}

	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.Limit = n
		}
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}

	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.Offset = n
		}
	}

	return p
}

// PaginatedResponse is a standard paginated list response.
type PaginatedResponse[T any] struct {
	Data    []T  `json:"data"`
	Total   int  `json:"total"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}

// NewPaginatedResponse constructs a paginated response.
//
//	videos, total, _ := queries.ListVideos(ctx, p.Limit, p.Offset)
//	handler.WriteJSON(w, 200, handler.NewPaginatedResponse(videos, total, p))
func NewPaginatedResponse[T any](data []T, total int, p Pagination) PaginatedResponse[T] {
	if data == nil {
		data = []T{}
	}
	return PaginatedResponse[T]{
		Data:    data,
		Total:   total,
		Limit:   p.Limit,
		Offset:  p.Offset,
		HasMore: p.Offset+len(data) < total,
	}
}
