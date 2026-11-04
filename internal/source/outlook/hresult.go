package outlook

// HRESULTs the connector acts on (winerror.h, mapicode.h; the COM and
// RPC codes as documented on Microsoft Learn, "COM Error Codes" and
// "System Error Codes (1700-3999)").
const (
	hrCallRejected      uint32 = 0x80010001 // RPC_E_CALL_REJECTED: Outlook is busy (modal dialog, …)
	hrRetryLater        uint32 = 0x8001010A // RPC_E_SERVERCALL_RETRYLATER
	hrServerRejected    uint32 = 0x8001010B // RPC_E_SERVERCALL_REJECTED: the callee rejected the call (busy)
	hrServerTooBusy     uint32 = 0x800706BB // RPC_S_SERVER_TOO_BUSY
	hrDisconnected      uint32 = 0x80010108 // RPC_E_DISCONNECTED: the object disconnected from its clients
	hrConnTerminated    uint32 = 0x80010006 // RPC_E_CONNECTION_TERMINATED
	hrServerDied        uint32 = 0x80010007 // RPC_E_SERVER_DIED: the callee disappeared (the call may have run)
	hrServerDiedDNE     uint32 = 0x80010012 // RPC_E_SERVER_DIED_DNE: the callee disappeared (the call did not run)
	hrServerFault       uint32 = 0x80010105 // RPC_E_SERVERFAULT: the server raised an exception (COM caught it)
	hrServerUnavailable uint32 = 0x800706BA // RPC_S_SERVER_UNAVAILABLE: the usual code once Outlook died
	hrCallFailed        uint32 = 0x800706BE // RPC_S_CALL_FAILED: the server died under the call
	hrCallFailedDNE     uint32 = 0x800706BF // RPC_S_CALL_FAILED_DNE
	hrObjNotConnected   uint32 = 0x800401FD // CO_E_OBJNOTCONNECTED
	hrServerStopping    uint32 = 0x80080008 // CO_E_SERVER_STOPPING: Outlook is shutting down
	hrUnavailable       uint32 = 0x800401E3 // MK_E_UNAVAILABLE: no running object
	hrServerExecFailure uint32 = 0x80080005 // CO_E_SERVER_EXEC_FAILURE: integrity mismatch
	hrClassString       uint32 = 0x800401F3 // CO_E_CLASSSTRING: ProgID not registered
	hrInvalidArg        uint32 = 0x80070057 // E_INVALIDARG (the fake's argument check)
	hrFail              uint32 = 0x80004005 // E_FAIL: Outlook's generic "the operation failed"
	hrNotFound          uint32 = 0x8004010F // MAPI_E_NOT_FOUND
	hrNetworkError      uint32 = 0x80040115 // MAPI_E_NETWORK_ERROR
	hrFailOneProvider   uint32 = 0x8004011D // MAPI_E_FAILONEPROVIDER
	hrLogonFailed       uint32 = 0x80040111 // MAPI_E_LOGON_FAILED
	hrUnknownName       uint32 = 0x80020006 // DISP_E_UNKNOWNNAME
	hrException         uint32 = 0x80020009 // DISP_E_EXCEPTION
)

// retryable: Outlook declined the call for now (busy); the same call may succeed later.
func retryable(hr uint32) bool {
	return hr == hrCallRejected || hr == hrRetryLater || hr == hrServerRejected || hr == hrServerTooBusy
}

// sessionLost: the Outlook process is gone, stopping or restarted, so
// every object is dead — or it raised an exception inside a call
// (RPC_E_SERVERFAULT: COM caught what would have killed it, and Outlook
// may be left corrupted). Either way the connector stops calling, and the
// breaker decides when to try again.
func sessionLost(hr uint32) bool {
	switch hr {
	case hrDisconnected, hrConnTerminated, hrServerDied, hrServerDiedDNE, hrServerFault, hrServerUnavailable,
		hrCallFailed, hrCallFailedDNE, hrObjNotConnected, hrServerStopping:
		return true
	}
	return false
}

// offline: a store's server cannot be reached (online delegate mailboxes).
func offline(hr uint32) bool {
	return hr == hrNetworkError || hr == hrFailOneProvider || hr == hrLogonFailed
}
