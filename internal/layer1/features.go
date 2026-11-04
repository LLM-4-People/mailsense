package layer1

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// The local design's second feature block: lexical, sender and metadata
// cells. The embedding captures meaning; these cells capture the concrete
// cues a small linear head learns fastest (a subject's vocabulary, the
// sender organization, a reply, an attachment). They are hashed into a fixed
// number of columns, deterministic and bounded, and appended after the
// evidence cells.
const (
	featureVersion = "v3"
	subjectBuckets = 256
	bodyBuckets    = 768
	domainBuckets  = 64
	senderBuckets  = 128
	metadataCells  = 16
	historyCells   = 66
	bodyHeadRunes  = 4000
)

// featureCells is one message's feature cells, offset by base (the
// evidence columns). The result is sorted and unique.
func featureCells(m Member, base int) []int32 {
	cells := make([]int32, 0, 24)
	for _, token := range featureTokens(m.Subject) {
		cells = append(cells, int32(base+hashToken("s:"+token, subjectBuckets)))
	}
	bodyBase := base + subjectBuckets
	for _, token := range featureTokens(mail.Head(mail.Strip(m.Body), bodyHeadRunes)) {
		cells = append(cells, int32(bodyBase+hashToken("b:"+token, bodyBuckets)))
	}
	domainBase := bodyBase + bodyBuckets
	if domain := mail.RegistrableDomain(m.SenderAddress); domain != "" {
		cells = append(cells, int32(domainBase+hashToken(domain, domainBuckets)))
	}
	senderBase := domainBase + domainBuckets
	if address := strings.ToLower(strings.TrimSpace(m.SenderAddress)); address != "" {
		cells = append(cells, int32(senderBase+hashToken(address, senderBuckets)))
	}
	metaBase := senderBase + senderBuckets
	addMeta := func(at int) {
		if at >= 0 && at < metadataCells {
			cells = append(cells, int32(metaBase+at))
		}
	}
	if m.HasAttachments {
		addMeta(0)
	}
	switch {
	case m.RecipientCount == 1:
		addMeta(1)
	case m.RecipientCount <= 3:
		addMeta(2)
	default:
		addMeta(3)
	}
	if m.IsReply {
		addMeta(4)
	}
	switch {
	case m.Depth == 1:
		addMeta(5)
	case m.Depth >= 2:
		addMeta(6)
	}
	switch {
	case m.Hour >= 0 && m.Hour < 7:
		addMeta(7)
	case m.Hour < 10:
		addMeta(8)
	case m.Hour < 13:
		addMeta(9)
	case m.Hour < 18:
		addMeta(10)
	default:
		addMeta(11)
	}
	if m.HasAttachments {
		addMeta(12)
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(m.Subject)), "re:") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(m.Subject)), "fw:") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(m.Subject)), "tr:") {
		addMeta(13)
	}
	if len(m.Body) > 2000 {
		addMeta(14)
	}
	if m.ListUnsubscribe {
		addMeta(15)
	}
	historyBase := metaBase + metadataCells
	// The causal history block: how much of this sender, domain and
	// conversation the mailbox already knew when this message arrived, and
	// how the two sides have treated each other. The buckets are coarse on
	// purpose: a small head learns a direction, not a precise count, and
	// coarse cells generalize across mailboxes.
	cells = append(cells, int32(historyBase+countBucket(m.SenderMessagesBefore)))
	cells = append(cells, int32(historyBase+8+countBucket(m.DomainMessagesBefore)))
	cells = append(cells, int32(historyBase+16+daysBucket(m.SenderFirstDays)))
	cells = append(cells, int32(historyBase+23+daysBucket(m.DomainFirstDays)))
	cells = append(cells, int32(historyBase+30+threadBucket(m.ThreadMessagesBefore)))
	if m.FromOwner {
		cells = append(cells, int32(historyBase+35))
	} else {
		cells = append(cells, int32(historyBase+36))
	}
	// Reciprocity: a sender the owner already wrote to is a correspondent;
	// a sender that only ever writes is a newsletter, vendor or prospect.
	cells = append(cells, int32(historyBase+37+countBucket(m.OwnerWroteCountBefore)))
	// Recency and current activity: a quiet correspondent and a burst of
	// mail from the same address mean different things.
	cells = append(cells, int32(historyBase+45+daysBucket(m.SenderDaysSinceLast)))
	cells = append(cells, int32(historyBase+52+countBucket(m.SenderMessagesLast30)))
	// Thread role: who has been speaking in this conversation, and whether
	// the message before this one was the owner's own.
	cells = append(cells, int32(historyBase+60+threadBucket(m.ThreadOwnerMessagesBefore)))
	if m.ThreadLastFromOwner {
		cells = append(cells, int32(historyBase+65))
	}
	sort.Slice(cells, func(i, j int) bool { return cells[i] < cells[j] })
	out := cells[:0]
	for i, cell := range cells {
		if i == 0 || cell != cells[i-1] {
			out = append(out, cell)
		}
	}
	return out
}

// featureCellsCount is the number of columns featureCells uses.
func featureCellsCount() int {
	return subjectBuckets + bodyBuckets + domainBuckets + senderBuckets + metadataCells + historyCells
}

// addHistory fills each member's causal history from the visible
// population in time order: messages from the same sender, registrable
// domain and conversation that arrived before it. It reads no label.
func addHistory(members []Member) {
	order := make([]int, len(members))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := members[order[i]], members[order[j]]
		if !a.Instant.Equal(b.Instant) {
			if a.Instant.IsZero() {
				return false
			}
			if b.Instant.IsZero() {
				return true
			}
			return a.Instant.Before(b.Instant)
		}
		return a.ID < b.ID
	})
	senderFirst, senderCount := map[string]time.Time{}, map[string]int{}
	domainFirst, domainCount := map[string]time.Time{}, map[string]int{}
	// The relationship block: what the mailbox already knew about the two
	// sides' dealings, all of it dated strictly before this message. The
	// thread block is not here: it comes from the causal thread snapshot
	// (addThreadHistory), so a later bridging message cannot rewrite it.
	senderLast := map[string]time.Time{}
	senderRecent := map[string][]time.Time{}
	ownerWroteTo := map[string]int{}
	for _, i := range order {
		m := &members[i]
		sender := strings.ToLower(strings.TrimSpace(m.SenderAddress))
		domain := mail.RegistrableDomain(m.SenderAddress)
		if sender != "" {
			m.SenderMessagesBefore = senderCount[sender]
			if first, ok := senderFirst[sender]; ok {
				m.SenderFirstDays = daysBetween(first, m.Instant)
			} else {
				senderFirst[sender] = m.Instant
			}
			if last, ok := senderLast[sender]; ok && !m.Instant.IsZero() {
				m.SenderDaysSinceLast = daysBetween(last, m.Instant)
			}
			if !m.Instant.IsZero() {
				senderLast[sender] = m.Instant
				recent := senderRecent[sender]
				cutoff := m.Instant.AddDate(0, 0, -30)
				kept := recent[:0]
				for _, at := range recent {
					if at.After(cutoff) {
						kept = append(kept, at)
					}
				}
				senderRecent[sender] = append(kept, m.Instant)
				m.SenderMessagesLast30 = len(kept)
			}
			senderCount[sender]++
		}
		if domain != "" {
			m.DomainMessagesBefore = domainCount[domain]
			if first, ok := domainFirst[domain]; ok {
				m.DomainFirstDays = daysBetween(first, m.Instant)
			} else {
				domainFirst[domain] = m.Instant
			}
			domainCount[domain]++
		}
		if m.FromOwner {
			for _, address := range m.Recipients {
				if address != "" {
					ownerWroteTo[address]++
				}
			}
		} else if sender != "" {
			m.OwnerWroteCountBefore = ownerWroteTo[sender]
		}
	}
}

// addThreadHistory fills the thread block from the conversation as it stood
// at the message's instant, in time order: how many messages preceded it,
// how many were the owner's own, and whether the owner spoke last. The
// caller supplies that snapshot (threads.Analysis.EarlierMembers), never the
// final thread groups, which a later bridging message can change.
func addThreadHistory(m *Member, fromOwnerBefore []bool) {
	m.ThreadMessagesBefore = len(fromOwnerBefore)
	for _, fromOwner := range fromOwnerBefore {
		if fromOwner {
			m.ThreadOwnerMessagesBefore++
		}
	}
	m.ThreadLastFromOwner = len(fromOwnerBefore) > 0 && fromOwnerBefore[len(fromOwnerBefore)-1]
}

// daysBetween is the whole days from a to b (never negative, 0 when
// either instant is unknown).
func daysBetween(a, b time.Time) int {
	if a.IsZero() || b.IsZero() {
		return 0
	}
	d := int(b.Sub(a).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}

// countBucket, daysBucket and threadBucket are the history block's coarse
// buckets: 0, 1, 2-3, 4-7, 8-15, 16-31, 32-63, 64+ for counts; 0, 1-3,
// 4-7, 8-30, 31-90, 91-365, 365+ for ages; 0, 1, 2-3, 4-7, 8+ for a
// conversation.
func countBucket(n int) int {
	switch {
	case n <= 0:
		return 0
	case n == 1:
		return 1
	case n <= 3:
		return 2
	case n <= 7:
		return 3
	case n <= 15:
		return 4
	case n <= 31:
		return 5
	case n <= 63:
		return 6
	default:
		return 7
	}
}

func daysBucket(d int) int {
	switch {
	case d <= 0:
		return 0
	case d <= 3:
		return 1
	case d <= 7:
		return 2
	case d <= 30:
		return 3
	case d <= 90:
		return 4
	case d <= 365:
		return 5
	default:
		return 6
	}
}

func threadBucket(n int) int {
	switch {
	case n <= 0:
		return 0
	case n == 1:
		return 1
	case n <= 3:
		return 2
	case n <= 7:
		return 3
	default:
		return 4
	}
}

// featureTokens folds and splits text into word tokens, dropping stopwords
// and one/two-letter noise. Duplicate tokens are kept once: the design cell
// is a presence bit.
func featureTokens(text string) []string {
	folded := mail.Fold(text)
	fields := strings.FieldsFunc(folded, func(r rune) bool { return !mail.IsWordRune(r) })
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		token := strings.TrimFunc(field, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if len(token) < 3 || featureStopwords[token] || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

// hashToken is FNV-1a 32-bit modulo buckets: deterministic across runs and
// architectures, bounded by the design.
func hashToken(token string, buckets int) int {
	const offset, prime = 2166136261, 16777619
	var h uint32 = offset
	for i := 0; i < len(token); i++ {
		h ^= uint32(token[i])
		h *= prime
	}
	return int(h % uint32(buckets))
}

// featureStopwords are the frame words every email repeats. They carry no
// topic on their own; the embedding already models their context.
var featureStopwords = wordSetFeatures("the and for with from this that these those you your our " +
	"de la le les des du et pour avec dans sur une un est sont au aux en par plus pas que qui " +
	"ce cette ces son sa ses leur leurs nous vous ils elles a an of to in on at by is are was " +
	"were be been being it its as or not no yes re fw tr")

func wordSetFeatures(folded string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(folded) {
		out[w] = true
	}
	return out
}
