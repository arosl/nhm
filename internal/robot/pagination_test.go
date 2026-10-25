// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/pagination_test.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header;
// TestPageSessions at the end is nhm's (nhm-2p6.2.4).

package robot

import (
	"reflect"
	"testing"
)

func TestApplyPagination_NoOptions(t *testing.T) {
	items := []int{1, 2, 3}

	got, page := ApplyPagination(items, PaginationOptions{})
	if page != nil {
		t.Fatalf("expected nil pagination info, got %+v", page)
	}
	if !reflect.DeepEqual(got, items) {
		t.Fatalf("expected %v, got %v", items, got)
	}
}

func TestApplyPagination_LimitOnly(t *testing.T) {
	items := []int{1, 2, 3}

	got, page := ApplyPagination(items, PaginationOptions{Limit: 2})
	if page == nil {
		t.Fatal("expected pagination info, got nil")
	}
	if !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("expected first two items, got %v", got)
	}
	if page.Limit != 2 || page.Offset != 0 || page.Count != 2 || page.Total != 3 {
		t.Fatalf("unexpected pagination info: %+v", page)
	}
	if !page.HasMore || page.NextCursor == nil || *page.NextCursor != 2 {
		t.Fatalf("expected next_cursor=2 and has_more=true, got %+v", page)
	}
}

func TestApplyPagination_OffsetOnly(t *testing.T) {
	items := []int{1, 2, 3}

	got, page := ApplyPagination(items, PaginationOptions{Offset: 2})
	if page == nil {
		t.Fatal("expected pagination info, got nil")
	}
	if !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("expected last item, got %v", got)
	}
	if page.Offset != 2 || page.Count != 1 || page.Total != 3 {
		t.Fatalf("unexpected pagination info: %+v", page)
	}
	if page.HasMore || page.NextCursor != nil {
		t.Fatalf("expected has_more=false and next_cursor nil, got %+v", page)
	}
}

func TestApplyPagination_LimitAndOffset(t *testing.T) {
	items := []int{1, 2, 3, 4}

	got, page := ApplyPagination(items, PaginationOptions{Limit: 2, Offset: 1})
	if page == nil {
		t.Fatal("expected pagination info, got nil")
	}
	if !reflect.DeepEqual(got, []int{2, 3}) {
		t.Fatalf("expected middle slice, got %v", got)
	}
	if page.Limit != 2 || page.Offset != 1 || page.Count != 2 || page.Total != 4 {
		t.Fatalf("unexpected pagination info: %+v", page)
	}
	if !page.HasMore || page.NextCursor == nil || *page.NextCursor != 3 {
		t.Fatalf("expected next_cursor=3 and has_more=true, got %+v", page)
	}
}

func TestApplyPagination_OffsetBeyondTotal(t *testing.T) {
	items := []int{1, 2, 3}

	got, page := ApplyPagination(items, PaginationOptions{Offset: 10})
	if page == nil {
		t.Fatal("expected pagination info, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
	if page.Offset != 3 || page.Count != 0 || page.Total != 3 {
		t.Fatalf("unexpected pagination info: %+v", page)
	}
	if page.HasMore || page.NextCursor != nil {
		t.Fatalf("expected has_more=false and next_cursor nil, got %+v", page)
	}
}

func TestApplyPagination_NegativeOffset(t *testing.T) {
	items := []int{1, 2, 3}

	got, page := ApplyPagination(items, PaginationOptions{Limit: 1, Offset: -5})
	if page == nil {
		t.Fatal("expected pagination info, got nil")
	}
	if !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("expected first item, got %v", got)
	}
	if page.Offset != 0 {
		t.Fatalf("expected offset clamped to 0, got %d", page.Offset)
	}
}

func TestPaginationHintOffsets(t *testing.T) {
	page := &PaginationInfo{
		Limit:   2,
		Offset:  0,
		Count:   2,
		Total:   5,
		HasMore: true,
	}
	next := 2
	page.NextCursor = &next

	nextOffset, pagesRemaining := paginationHintOffsets(page)
	if nextOffset == nil || *nextOffset != 2 {
		t.Fatalf("expected next_offset=2, got %+v", nextOffset)
	}
	if pagesRemaining == nil || *pagesRemaining != 2 {
		t.Fatalf("expected pages_remaining=2, got %+v", pagesRemaining)
	}
}

func TestPaginationHintOffsets_Nil(t *testing.T) {
	nextOffset, pagesRemaining := paginationHintOffsets(nil)
	if nextOffset != nil {
		t.Errorf("expected nil nextOffset for nil page, got %v", *nextOffset)
	}
	if pagesRemaining != nil {
		t.Errorf("expected nil pagesRemaining for nil page, got %v", *pagesRemaining)
	}
}

func TestPaginationHintOffsets_ZeroLimit(t *testing.T) {
	page := &PaginationInfo{
		Limit:  0,
		Offset: 0,
		Count:  5,
		Total:  5,
	}
	nextOffset, pagesRemaining := paginationHintOffsets(page)
	if nextOffset != nil {
		t.Errorf("expected nil nextOffset for zero limit, got %v", *nextOffset)
	}
	if pagesRemaining != nil {
		t.Errorf("expected nil pagesRemaining for zero limit, got %v", *pagesRemaining)
	}
}

func TestPaginationHintOffsets_NegativeLimit(t *testing.T) {
	page := &PaginationInfo{
		Limit:  -1,
		Offset: 0,
		Count:  3,
		Total:  3,
	}
	nextOffset, pagesRemaining := paginationHintOffsets(page)
	if nextOffset != nil || pagesRemaining != nil {
		t.Error("expected nil results for negative limit")
	}
}

func TestPaginationHintOffsets_LastPage(t *testing.T) {
	page := &PaginationInfo{
		Limit:  3,
		Offset: 6,
		Count:  2,
		Total:  8,
	}
	nextOffset, pagesRemaining := paginationHintOffsets(page)
	if nextOffset == nil || *nextOffset != 8 {
		t.Errorf("expected next_offset=8, got %+v", nextOffset)
	}
	if pagesRemaining == nil || *pagesRemaining != 0 {
		t.Errorf("expected pages_remaining=0, got %+v", pagesRemaining)
	}
}

func TestPaginationHintOffsets_SinglePage(t *testing.T) {
	page := &PaginationInfo{
		Limit:  10,
		Offset: 0,
		Count:  3,
		Total:  3,
	}
	nextOffset, pagesRemaining := paginationHintOffsets(page)
	if nextOffset == nil || *nextOffset != 3 {
		t.Errorf("expected next_offset=3, got %+v", nextOffset)
	}
	if pagesRemaining == nil || *pagesRemaining != 0 {
		t.Errorf("expected pages_remaining=0, got %+v", pagesRemaining)
	}
}

func TestApplyPagination_EmptySlice(t *testing.T) {
	var items []string
	got, page := ApplyPagination(items, PaginationOptions{Limit: 5})
	if page == nil {
		t.Fatal("expected pagination info, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
	if page.Total != 0 || page.Count != 0 {
		t.Errorf("expected total=0, count=0; got total=%d, count=%d", page.Total, page.Count)
	}
	if page.HasMore || page.NextCursor != nil {
		t.Error("expected has_more=false and next_cursor=nil for empty slice")
	}
}

func TestApplyPagination_LimitExceedsTotal(t *testing.T) {
	items := []string{"a", "b"}
	got, page := ApplyPagination(items, PaginationOptions{Limit: 100})
	if page == nil {
		t.Fatal("expected pagination info, got nil")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 items, got %d", len(got))
	}
	if page.HasMore {
		t.Error("expected has_more=false when limit exceeds total")
	}
}

func TestApplyPagination_LargePayloadDeterministicNextCursor(t *testing.T) {
	items := make([]int, 105)
	for i := range items {
		items[i] = i + 1
	}

	got, page := ApplyPagination(items, PaginationOptions{Limit: 100})
	if page == nil {
		t.Fatal("expected pagination info, got nil")
	}
	t.Logf("ROBOT_TEST: page limit=%d count=%d next=%v", page.Limit, page.Count, page.NextCursor)

	if len(got) != 100 {
		t.Fatalf("expected 100 items, got %d", len(got))
	}
	if page.Total != 105 || page.Count != 100 {
		t.Fatalf("unexpected pagination info: %+v", page)
	}
	if !page.HasMore || page.NextCursor == nil || *page.NextCursor != 100 {
		t.Fatalf("expected next_cursor=100 and has_more=true, got %+v", page)
	}
}

// PageSessions is statusFinalize's paging step: no paging leaves the list
// and gives no blocks; a page gives ntm's _agent_hints offsets.
func TestPageSessions(t *testing.T) {
	items := []string{"a", "b", "c"}
	got, page, hints := PageSessions(items, PaginationOptions{})
	if !reflect.DeepEqual(got, items) || page != nil || hints != nil {
		t.Fatalf("no paging = %v %+v %+v", got, page, hints)
	}
	got, page, hints = PageSessions(items, PaginationOptions{Limit: 2})
	if !reflect.DeepEqual(got, []string{"a", "b"}) || page == nil || page.Total != 3 || hints == nil ||
		*hints.NextOffset != 2 || *hints.PagesRemaining != 1 {
		t.Fatalf("limit 2 = %v %+v %+v", got, page, hints)
	}
	if _, _, hints = PageSessions(items, PaginationOptions{Offset: 9}); hints != nil {
		t.Fatalf("past the end: hints %+v, want none (limit 0)", hints)
	}
}
