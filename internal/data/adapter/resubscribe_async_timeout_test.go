package adapter

// resubscribeAll 逐符号超时 + 挪开流后异步——订阅楔形不再阻塞流建立。
// 锚：fake Subscribe 阻塞 >timeout → OnQuote 流仍开出；超时后调用返回（goroutine 收）。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// blockSubClient Subscribe 阻塞至 ctx 超时（楔死演练）；done 记超时返回。
type blockSubClient struct {
	*fakeMT5Client
	calls atomic.Int32
	done  atomic.Int32
}

func (b *blockSubClient) Subscribe(ctx context.Context, _ *mt5pb.SubscribeRequest, _ ...grpc.CallOption) (*mt5pb.SubscribeReply, error) {
	b.calls.Add(1)
	<-ctx.Done()
	b.done.Add(1)
	return nil, ctx.Err()
}

func TestD563_ResubscribeAsyncTimeout(t *testing.T) {
	old := resubscribeCallTimeout
	resubscribeCallTimeout = 60 * time.Millisecond
	defer func() { resubscribeCallTimeout = old }()

	fake := &fakeMT5Client{}
	fake.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{
		newFakeQuoteStream(context.Background(), nil, nil), // 阻塞流
	}
	bs := &blockSubClient{fakeMT5Client: fake}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &MT5Adapter{venue: "vq", mt5: fake, subs: bs, streams: fake, token: "tok", stopCtx: ctx, stopCancel: cancel}

	ch, c2 := a.Subscribe([]string{"XAUUSD", "EURUSD"})
	_ = ch
	defer c2()

	// OnQuote 流开出不等订阅批（旧序=先同步 resubscribeAll 楔死后才开流）。
	if !waitFor(t, 2*time.Second, func() bool { return fake.onQuoteCallsLoad() >= 1 }) {
		t.Fatalf("订阅阻塞下 OnQuote 应仍开出, calls=%d", fake.onQuoteCallsLoad())
	}
	if !waitFor(t, 2*time.Second, func() bool { return bs.calls.Load() >= 1 }) {
		t.Fatal("resubscribeAll 应已发起")
	}
	// 逐符号超时返回（goroutine 不永久挂）。
	if !waitFor(t, 2*time.Second, func() bool { return bs.done.Load() >= 1 }) {
		t.Fatal("阻塞订阅应经 5s→(测)60ms 超时返回")
	}
}
