package threads

// Version is the version of what this package computes — conversations,
// reply matching, histories, the behaviour profile. Views computed from
// them (evidence's thread and history signals) carry it, so a change of
// these rules recomputes them. Raised with every such change (2: copies of
// a message — same store, same Message-ID — count once).
const Version = "2"
