package ui

import "testing"

// The topic coverage line must render whenever a topic intent carries a
// topic, including the 0/0 census: hiding it is exactly the silent
// uncertified zero the disclosure exists to prevent. A scanned census shows
// the classified-over-total sentence instead.
func TestAskTopicCoverageDisclosesAnUncertifiedZero(t *testing.T) {
	runUISandbox(t, "ask_topic_coverage", askTopicCoverageHarness, "ask topic coverage behavior: ok")
}

const askTopicCoverageHarness = askCardHarness + `
function topicView(scanned, classified) {
  return {
    id: 1, question: "How many invoices?", mode: "structured", intent: "topic",
    topic: { topic: "billing_account", subtopic: "invoice", count: 0, classified, scanned },
    citations: [], took_ms: 0,
  };
}

const zeroCard = sandbox.askAnswerCard(topicView(0, 0));
const zeroText = texts(zeroCard).join(" | ");
assert.ok(zeroText.includes("No message was scanned yet"), "the uncertified zero is hidden: " + zeroText);
assert.ok(!zeroText.includes("0 of 0"), "the zero census still renders as coverage: " + zeroText);

const coveredCard = sandbox.askAnswerCard(topicView(120, 87));
const coveredText = texts(coveredCard).join(" | ");
assert.ok(coveredText.includes("87 of 120 messages have a topic"), "the coverage line is missing: " + coveredText);

const untopicedCard = sandbox.askAnswerCard({
  id: 2, question: "What is waiting?", mode: "structured", intent: "workload",
  counts: { workload: 3 }, citations: [], took_ms: 0,
});
assert.ok(!texts(untopicedCard).join(" | ").includes("scanned"),
  "a non-topic answer renders the topic coverage line");

console.log("ask topic coverage behavior: ok");
`
