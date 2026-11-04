// Package outlook is the classic Outlook (local COM) connector: every mail
// folder of every store of the running profile, read-only.
//
// Layers, top down (docs/OUTLOOK_SYNC.md is the design and the measured
// cost model):
//
//   - Connector (connector.go): source.Connector + source.DetailReader.
//     Discover lists stores and mail folders and reads each folder's change
//     state; Sync diffs an id/stamp snapshot against the store and streams
//     the changed rows of one sorted table pass as partial messages;
//     ReadDetails opens one item for body, headers and recipients.
//   - guard (guard.go): wraps every object the connector touches. It
//     allows only the members listed in model.go (read-only by
//     construction: no Save, Delete, Move, Send, no property puts), retries
//     RPC_E_CALL_REJECTED / RPC_E_SERVERCALL_RETRYLATER with the configured
//     backoff and counts calls.
//   - Object (object.go): one late-bound object. On Windows a COM IDispatch
//     (dispatch_windows.go) living on the connector's single STA thread
//     (apartment.go); in tests the fake object model of fake_test.go.
//
// Everything above Object is pure Go and runs on every OS against the fake.
package outlook
