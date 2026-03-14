package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"opencoderouter/internal/cache"
)

const defaultScrollbackLimit = 1000

type ScrollbackHandler struct {
	cache cache.ScrollbackCache
}

type scrollbackQuery struct {
	offset int
	limit  int
	typeV  cache.EntryType
}

func NewScrollbackHandler(scrollbackCache cache.ScrollbackCache) *ScrollbackHandler {
	return &ScrollbackHandler{cache: scrollbackCache}
}

func (h *ScrollbackHandler) HandleGet(w http.ResponseWriter, r *http.Request, sessionID string) {
	if h == nil || h.cache == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "scrollback cache unavailable", "SCROLLBACK_UNAVAILABLE")
		return
	}

	query, err := parseScrollbackQuery(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "INVALID_SCROLLBACK_QUERY")
		return
	}

	entries, err := h.getFiltered(sessionID, query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to read scrollback", "SCROLLBACK_READ_FAILED")
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

func (h *ScrollbackHandler) getFiltered(sessionID string, query scrollbackQuery) ([]cache.Entry, error) {
	if query.typeV == "" {
		return h.cache.Get(sessionID, query.offset, query.limit)
	}

	all, err := h.cache.Get(sessionID, 0, 0)
	if err != nil {
		return nil, err
	}

	filtered := make([]cache.Entry, 0, len(all))
	for _, entry := range all {
		if entry.Type == query.typeV {
			filtered = append(filtered, entry)
		}
	}

	if query.offset >= len(filtered) {
		return []cache.Entry{}, nil
	}

	end := len(filtered)
	if query.offset+query.limit < end {
		end = query.offset + query.limit
	}

	result := make([]cache.Entry, end-query.offset)
	copy(result, filtered[query.offset:end])
	return result, nil
}

func parseScrollbackQuery(r *http.Request) (scrollbackQuery, error) {
	q := r.URL.Query()
	result := scrollbackQuery{limit: defaultScrollbackLimit}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return scrollbackQuery{}, errors.New("limit must be a positive integer")
		}
		result.limit = limit
	}

	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return scrollbackQuery{}, errors.New("offset must be a non-negative integer")
		}
		result.offset = offset
	}

	if raw := strings.TrimSpace(q.Get("type")); raw != "" {
		result.typeV = cache.EntryType(raw)
	}

	return result, nil
}
