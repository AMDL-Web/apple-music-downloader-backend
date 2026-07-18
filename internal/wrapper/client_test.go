package wrapper

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"amdl/internal/config"
	pb "github.com/AMDL-Web/wrapper-manager/proto"
	"google.golang.org/grpc"
)

// blockingM3U8API parks every M3U8 call until release is closed so tests can
// hold data-slot permits at will. All other RPCs panic via the embedded nil.
type blockingM3U8API struct {
	pb.WrapperManagerServiceClient
	inFlight atomic.Int32
	entered  chan struct{}
	release  chan struct{}
}

func (f *blockingM3U8API) M3U8(ctx context.Context, _ *pb.M3U8Request, _ ...grpc.CallOption) (*pb.M3U8Reply, error) {
	f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	select {
	case f.entered <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return &pb.M3U8Reply{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestDataConcurrencyLimitBoundsDataRPCs(t *testing.T) {
	api := &blockingM3U8API{entered: make(chan struct{}, 1), release: make(chan struct{})}
	client := &Client{api: api, cfg: config.WrapperConfig{TimeoutSeconds: 5}, sessions: map[string]*loginSession{}}
	WithDataConcurrencyLimit(1)(client)

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = client.M3U8(context.Background(), "1")
	}()
	<-api.entered

	// The pool has a single permit and it is held by the blocked first call: a
	// second call must wait on the semaphore and honor cancellation without
	// ever reaching the RPC layer.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.M3U8(ctx, "2"); err != context.DeadlineExceeded {
		t.Fatalf("second M3U8 error = %v, want context.DeadlineExceeded from the data slot wait", err)
	}
	if got := api.inFlight.Load(); got != 1 {
		t.Fatalf("in-flight RPCs = %d, want the second call blocked before the RPC layer", got)
	}

	close(api.release)
	<-firstDone
	// The permit must be released after the first call returns.
	if _, err := client.M3U8(context.Background(), "3"); err != nil {
		t.Fatalf("M3U8 after release: %v", err)
	}
}

func TestWrapperTransportCredentials(t *testing.T) {
	tests := []struct {
		name             string
		insecure         bool
		securityProtocol string
	}{
		{name: "plaintext", insecure: true, securityProtocol: "insecure"},
		{name: "TLS", insecure: false, securityProtocol: "tls"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creds := wrapperTransportCredentials(config.WrapperConfig{Insecure: tt.insecure})
			if got := creds.Info().SecurityProtocol; got != tt.securityProtocol {
				t.Fatalf("security protocol = %q, want %q", got, tt.securityProtocol)
			}
		})
	}
}
