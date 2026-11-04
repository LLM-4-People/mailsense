package mail

import "testing"

// Review 1a rules bug 4: error order followed map iteration.
func TestValidateErrorOrderIsDeterministic(t *testing.T) {
	m := Message{SourceID: "s", ID: "1", FolderPath: "p", FolderRole: RoleInbox,
		ReceivedAt: "x", SentOn: "y", CreationTime: "z", ReminderTime: "w", TaskDueDate: "v"}
	first := m.Validate().Error()
	for range 50 {
		if got := m.Validate().Error(); got != first {
			t.Fatalf("order changed:\n%s\n%s", first, got)
		}
	}
}
