package api

import (
	"net/http"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/layer1"
)

type labelBatchRequest struct {
	Items []feedback.PutItem `json:"items"`
}

func (s *Server) handleLabelQueue(w http.ResponseWriter, r *http.Request) {
	query, err := queryLabel(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	queue, err := s.labelQueueFor(r, query)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, queue)
}

// queryLabel is the one shape parser of the label-queue query, shared by the
// queue route and the message binding that proves a row belongs to that page:
// the limit is bounded, the review flag is strict, and the question is passed
// through for the queue's own required-field rule.
func queryLabel(r *http.Request) (layer1.LabelQuery, error) {
	limit, err := boundedQueryInt(r, "limit", 0, 0, layer1.LabelQueueMax)
	if err != nil {
		return layer1.LabelQuery{}, err
	}
	review, err := queryReview(r)
	if err != nil {
		return layer1.LabelQuery{}, err
	}
	return layer1.LabelQuery{Question: r.URL.Query().Get("question"), Limit: limit, Review: review}, nil
}

// queryReview reads the review flag: true/1 turns review mode on, false/0
// or an absent (or empty) value leaves it off.
func queryReview(r *http.Request) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("review"))) {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	}
	return false, &APIError{Status: http.StatusBadRequest, Code: "invalid_review",
		Message: "review must be true, 1, false or 0"}
}

// labelQueueFor applies the queue's required-field rule and asks the labels
// service. The shape rules (limit bound, review flag) belong to queryLabel.
func (s *Server) labelQueueFor(r *http.Request, query layer1.LabelQuery) (layer1.LabelQueue, error) {
	if query.Question == "" {
		return layer1.LabelQueue{}, invalidRequest("question is required")
	}
	return s.services.Labels.LabelQueue(r.Context(), query)
}

func (s *Server) handleLabelBatch(w http.ResponseWriter, r *http.Request) {
	var req labelBatchRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	// Only the batch's shape and bound are the request's: they are cheap and
	// stay 400 invalid_request. Every per-item rule (a positive message_id,
	// a known question, a value, no duplicate, a visible message) belongs to
	// the labeler, whose typed ValidationError maps to one 422 problem per
	// item. Re-checking an item here would drift from the service's rules.
	if len(req.Items) == 0 {
		s.writeError(w, r, invalidRequest("items is required"))
		return
	}
	if len(req.Items) > feedback.MaxLabelBatch {
		s.writeError(w, r, invalidRequest(feedback.BatchSizeProblem(len(req.Items))))
		return
	}
	result, err := s.services.Labels.PutLabels(r.Context(), req.Items)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, result)
}
