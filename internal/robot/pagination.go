// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/pagination.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: this header; PageSessions
// at the end is nhm's (nhm-2p6.2.4).

package robot

// PaginationOptions configures limit/offset pagination.
type PaginationOptions struct {
	Limit  int
	Offset int
}

// PaginationInfo describes pagination state for array outputs.
type PaginationInfo struct {
	Limit      int  `json:"limit"`
	Offset     int  `json:"offset"`
	Count      int  `json:"count"`
	Total      int  `json:"total"`
	HasMore    bool `json:"has_more"`
	NextCursor *int `json:"next_cursor,omitempty"`
}

// ApplyPagination slices items based on limit/offset and returns pagination metadata.
func ApplyPagination[T any](items []T, opts PaginationOptions) ([]T, *PaginationInfo) {
	if opts.Limit <= 0 && opts.Offset <= 0 {
		return items, nil
	}

	total := len(items)
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = total - offset
	}

	end := offset + limit
	if end > total {
		end = total
	}

	paged := items[offset:end]
	hasMore := end < total
	var next *int
	if hasMore {
		n := end
		next = &n
	}

	info := &PaginationInfo{
		Limit:      limit,
		Offset:     offset,
		Count:      len(paged),
		Total:      total,
		HasMore:    hasMore,
		NextCursor: next,
	}

	return paged, info
}

// PaginationAgentHints is the shared _agent_hints payload for paginated list
// surfaces (D1, bd-ws3-contract-breadth-psvyu.1): next_offset tells an agent
// exactly what --offset to pass for the next page.
type PaginationAgentHints struct {
	NextOffset     *int `json:"next_offset,omitempty"`
	PagesRemaining *int `json:"pages_remaining,omitempty"`
}

// PaginationHints derives the shared agent hints from pagination state.
// Returns nil when there is no further page to fetch.
func PaginationHints(page *PaginationInfo) *PaginationAgentHints {
	if page == nil || !page.HasMore {
		return nil
	}
	next, pages := paginationHintOffsets(page)
	if next == nil {
		return nil
	}
	return &PaginationAgentHints{NextOffset: next, PagesRemaining: pages}
}

func paginationHintOffsets(page *PaginationInfo) (*int, *int) {
	if page == nil || page.Limit <= 0 {
		return nil, nil
	}

	next := page.Offset + page.Count
	remaining := page.Total - next
	if remaining < 0 {
		remaining = 0
	}

	pages := 0
	if remaining > 0 {
		pages = (remaining + page.Limit - 1) / page.Limit
	}

	return &next, &pages
}

// PageSessions is the paging step of ntm's statusFinalize and
// snapshotFinalize (internal/robot/robot.go): it pages sessions after the
// summary is built, so the summary counts every session, and gives the
// pagination block and the _agent_hints with the next offset. Both are
// nil when opts asks for no paging.
func PageSessions[T any](sessions []T, opts PaginationOptions) ([]T, *PaginationInfo, *AgentHints) {
	paged, page := ApplyPagination(sessions, opts)
	if page == nil {
		return sessions, nil, nil
	}
	var hints *AgentHints
	if next, pages := paginationHintOffsets(page); next != nil {
		hints = &AgentHints{NextOffset: next, PagesRemaining: pages}
	}
	return paged, page, hints
}
