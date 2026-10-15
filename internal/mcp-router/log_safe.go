package mcprouter

import (
	"log/slog"
	"strings"

	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
)

// logSafeResponse renders an ext_proc response for logging without header
// values or bodies, which can carry session IDs and credentials. evaluated
// lazily so disabled debug logging costs nothing.
type logSafeResponse struct {
	r *extProcV3.ProcessingResponse
}

func (l logSafeResponse) LogValue() slog.Value {
	var (
		phase  string
		common *extProcV3.CommonResponse
	)
	switch {
	case l.r.GetRequestHeaders() != nil:
		phase, common = "request_headers", l.r.GetRequestHeaders().GetResponse()
	case l.r.GetRequestBody() != nil:
		phase, common = "request_body", l.r.GetRequestBody().GetResponse()
	case l.r.GetResponseHeaders() != nil:
		phase, common = "response_headers", l.r.GetResponseHeaders().GetResponse()
	case l.r.GetResponseBody() != nil:
		phase, common = "response_body", l.r.GetResponseBody().GetResponse()
	case l.r.GetImmediateResponse() != nil:
		ir := l.r.GetImmediateResponse()
		return slog.GroupValue(
			slog.String("phase", "immediate_response"),
			slog.Int("status", int(ir.GetStatus().GetCode())),
			slog.Bool("body", len(ir.GetBody()) > 0),
		)
	default:
		return slog.GroupValue(slog.String("phase", "other"))
	}

	mutation := common.GetHeaderMutation()
	set := make([]string, 0, len(mutation.GetSetHeaders()))
	session := ""
	for _, h := range mutation.GetSetHeaders() {
		key := h.GetHeader().GetKey()
		set = append(set, key)
		if strings.EqualFold(key, routing.SessionHeader) {
			v := h.GetHeader().GetValue()
			if v == "" {
				v = string(h.GetHeader().GetRawValue())
			}
			session = internaljwt.LogSafeSessionID(v)
		}
	}
	attrs := []slog.Attr{
		slog.String("phase", phase),
		slog.Any("set_headers", set),
		slog.Any("remove_headers", mutation.GetRemoveHeaders()),
		slog.Bool("body_mutated", common.GetBodyMutation() != nil),
		slog.Bool("clear_route_cache", common.GetClearRouteCache()),
	}
	if session != "" {
		attrs = append(attrs, slog.String("session", session))
	}
	return slog.GroupValue(attrs...)
}
