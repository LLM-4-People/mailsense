package agenda

import "testing"

// The agenda budget admits a result with the maximum item count and its
// reasons: the byte bound is headroom, not a second, tighter cap.
func TestAgendaBudgetAdmitsMaxItems(t *testing.T) {
	r := Result{Items: make([]Item, MaxItems)}
	for i := range r.Items {
		r.Items[i].Reasons = []Reason{ReasonMeeting, ReasonWaitingSince}
	}
	if got := weighResult(r); got > cacheMaxBytes {
		t.Fatalf("%d items weigh %d bytes, over the %d-byte budget", MaxItems, got, cacheMaxBytes)
	}
}
