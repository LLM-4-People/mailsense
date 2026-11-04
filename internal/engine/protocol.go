package engine

import (
	"fmt"
	"maps"
	"slices"
)

// Protocol names. A record names one; the protocol descriptor is the only
// place its wire behavior (auth, probe, required fields) is defined.
const (
	ProtocolSystemOne        = "systemone"
	ProtocolOpenAIDecisions  = "openai_decisions"
	ProtocolOpenAIChat       = "openai_chat"
	ProtocolOpenAIEmbeddings = "openai_embeddings"
)

// AuthScheme is how a protocol authenticates a request.
type AuthScheme string

// AuthBearer sends the key as "Authorization: Bearer <key>".
const AuthBearer AuthScheme = "bearer"

// Probe is the request of the decision provider test.
type Probe struct {
	State    string   `json:"state"`
	Question Question `json:"question"`
}

// Protocol is the wire behavior of one provider protocol: its engine kind,
// auth scheme, the record fields it requires, and (for deciders) the typed
// probe the test action sends. Endpoints, models, keys, limits and prices
// are record data, never protocol code.
type Protocol struct {
	Name           string
	Kind           Kind
	Auth           AuthScheme
	KeyRequired    bool
	RuntimeAllowed bool
	Probe          *Probe
}

// deciderProbe is the request the provider test action sends to a decision
// provider: one tiny typed question about a connection-test state.
var deciderProbe = Probe{
	State: "Subject: connection test\n\nThis message only checks that the decision provider answers.",
	Question: Question{
		Name:         "is_test",
		Type:         Noul,
		Instructions: "Is this message a connection test?",
	},
}

var protocols = map[string]Protocol{
	ProtocolSystemOne: {
		Name:        ProtocolSystemOne,
		Kind:        KindDecider,
		Auth:        AuthBearer,
		KeyRequired: true,
		Probe:       &deciderProbe,
	},
	ProtocolOpenAIDecisions: {
		Name:        ProtocolOpenAIDecisions,
		Kind:        KindDecider,
		Auth:        AuthBearer,
		KeyRequired: true,
		Probe:       &deciderProbe,
	},
	ProtocolOpenAIChat: {
		Name:        ProtocolOpenAIChat,
		Kind:        KindGenerator,
		Auth:        AuthBearer,
		KeyRequired: true,
	},
	ProtocolOpenAIEmbeddings: {
		Name:           ProtocolOpenAIEmbeddings,
		Kind:           KindEmbedder,
		Auth:           AuthBearer,
		KeyRequired:    false,
		RuntimeAllowed: true,
	},
}

// ProtocolOf returns the descriptor of a protocol.
func ProtocolOf(name string) (Protocol, bool) {
	p, ok := protocols[name]
	return p, ok
}

// Protocols returns every protocol, sorted by name.
func Protocols() []Protocol {
	out := make([]Protocol, 0, len(protocols))
	for _, name := range slices.Sorted(maps.Keys(protocols)) {
		out = append(out, protocols[name])
	}
	return out
}

// ProtocolNames lists every protocol name, sorted. It is the one vocabulary
// of the validator's protocol enum and of the record schema.
func ProtocolNames() []string { return slices.Sorted(maps.Keys(protocols)) }

// protocolProblems validates the static protocol table (a programming
// error, reported like any other provider problem).
func protocolProblems() []string {
	var out []string
	for name, p := range protocols {
		if p.Name != name {
			out = append(out, fmt.Sprintf("protocol %q: name is %q", name, p.Name))
		}
		if !p.Kind.valid() {
			out = append(out, fmt.Sprintf("protocol %q: invalid kind %q", name, p.Kind))
		}
		switch p.Auth {
		case AuthBearer:
		default:
			out = append(out, fmt.Sprintf("protocol %q: invalid auth scheme %q", name, p.Auth))
		}
		if p.Probe != nil {
			if p.Kind != KindDecider {
				out = append(out, fmt.Sprintf("protocol %q: only a decider has a probe", name))
			}
			if p.Probe.State == "" {
				out = append(out, fmt.Sprintf("protocol %q: probe state is empty", name))
			}
			if err := p.Probe.Question.Validate(); err != nil {
				out = append(out, fmt.Sprintf("protocol %q: probe question: %v", name, err))
			}
		}
	}
	slices.Sort(out)
	return out
}
