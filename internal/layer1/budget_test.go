package layer1

import "testing"

// The label caches' budgets admit a queue at its maximum: LabelQueueMax
// tasks with a decoded suggestion stay under the queue budget, and the same
// task slice under the ranking budget.
func TestQueueBudgetsAdmitMaxQueues(t *testing.T) {
	tasks := make([]LabelTask, LabelQueueMax)
	for i := range tasks {
		tasks[i] = LabelTask{
			MessageID:        int64(i),
			Question:         "category",
			Reason:           "the model abstained",
			Suggestion:       "newsletter",
			SuggestionSource: "judge",
		}
	}
	queue := LabelQueue{Question: "category", Strategy: LabelStrategyUncertainty, Tasks: tasks}
	if got := weighLabelQueue(queue); got > labelQueueMaxBytes {
		t.Fatalf("a %d-task queue weighs %d bytes, over the %d-byte budget", len(tasks), got, labelQueueMaxBytes)
	}
	if got := weighLabelTasks(tasks); got > labelOrderMaxBytes {
		t.Fatalf("a %d-task order weighs %d bytes, over the %d-byte budget", len(tasks), got, labelOrderMaxBytes)
	}
}
