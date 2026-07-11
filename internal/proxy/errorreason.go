package proxy

// reasonCode maps a classified request outcome to a compact, groupable reason
// code persisted in usage_logs.error_reason (contracts/error-reason.md). Every
// value is <=32 bytes. Raw upstream error text is intentionally NOT stored: a
// 32-byte truncation of a JSON error body is unusable for aggregation and risks
// leaking secret/PII fragments.
//
// quotaBody distinguishes a 403 that indicates quota/balance exhaustion from a
// generic forbidden 403.
func reasonCode(class ErrorClass, httpCode int, quotaBody bool) string {
	switch class {
	case ErrNone:
		return "" // 2xx / no error — the common case
	case ErrClient:
		return "client_4xx"
	case ErrAuth:
		return "auth_401"
	case ErrForbidden:
		if quotaBody {
			return "quota_403"
		}
		return "forbidden_403"
	case ErrRateLimit:
		return "rate_limit"
	case ErrServer:
		return "server_5xx"
	case ErrTransport:
		return "transport"
	case ErrCanceled:
		return "canceled"
	default:
		if httpCode >= 200 && httpCode < 300 {
			return ""
		}
		return "unknown"
	}
}
