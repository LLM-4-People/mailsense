package priority

// Topic returns the census row for one topic id. ok is false when the
// classified mail carries no message for that topic: an absent row is a real
// zero, never a fabricated count, so the caller cannot mistake an unread
// census for an empty topic.
func (c TopicCensus) Topic(id string) (TopicCount, bool) {
	for _, row := range c.Topics {
		if row.Topic == id {
			return row, true
		}
	}
	return TopicCount{}, false
}

// Subtopic returns one topic row's subtopic rollup. ok is false when that
// subtopic has no classified mail inside the topic.
func (t TopicCount) Subtopic(id string) (SubtopicCount, bool) {
	for _, sub := range t.Subtopics {
		if sub.Subtopic == id {
			return sub, true
		}
	}
	return SubtopicCount{}, false
}
