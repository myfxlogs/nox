// service_stream_init.go —— D-508 S9 行数折分（自 service_stream.go 搬出，纯搬家零改动）。
package dashboard

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	v1 "arb/internal/gen/dashboardv1"
)

// streamInitQuotes D-090/D-508 S9 拆出：流启动初始化——缓存未初始化则先查库填缓存；首帧快照逐条 Send。
func (s *Service) streamInitQuotes(ctx context.Context, stream *connect.ServerStream[v1.StreamUpdatesResponse]) error {
	if err := s.ensureWatchlistLoaded(ctx); err != nil {
		return fmt.Errorf("stream init watchlist: %w", err)
	}
	for _, q := range s.qbus.AllLatest() {
		if !s.watchlistHitQuote(q) {
			continue
		}
		if err := stream.Send(&v1.StreamUpdatesResponse{
			Update: &v1.StreamUpdatesResponse_Quote{Quote: quoteToProto(q)},
		}); err != nil {
			return err
		}
	}
	return nil
}
