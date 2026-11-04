package store

// SettingSyncBatchSize is the number of messages written per store
// transaction. The store owns the key because it sizes the write batch; the
// syncer's folder and scope passes read the same value for their own writes.
// It lives here, not in the syncer, so the store does not have to import its
// caller to name it.
const SettingSyncBatchSize = "sync.batch_size"

// The bounds the store enforces and the search path validates. They are
// declared here because the store is the lower package and internal/search
// imports it: one name per key, so a ceiling cannot be typed in one reader and
// misspelled in the other.
const (
	SettingMaxOpenConns        = "store.max_open_conns"
	SettingQueryMaxOpenConns   = "query.max_open_conns"
	SettingQueryMaxRows        = "query.max_rows"
	SettingQueryTimeout        = "query.timeout"
	SettingSearchMaxHits       = "search.max_hits"
	SettingSearchCountCap      = "search.count_cap"
	SettingSearchLimit         = "search.limit"
	SettingSearchQueryMaxChars = "search.query_max_chars"
	SettingSearchEmbedTimeout  = "search.embed_timeout"
	SettingSearchRRFK          = "search.rrf_k"
	SettingSearchSnippetChars  = "search.snippet_chars"
)
