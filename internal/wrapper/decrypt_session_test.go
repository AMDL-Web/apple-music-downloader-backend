package wrapper

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/AMDL-Web/wrapper-manager/proto"
	"google.golang.org/grpc/metadata"
)

// fakeDecryptStream is a minimal pb.WrapperManagerService_DecryptClient whose
// Send blocks until the session context is cancelled, modelling a send stuck on
// the gRPC flow-control window, and whose Recv fails immediately.
type fakeDecryptStream struct {
	ctx         context.Context
	recvErr     error
	sendReturns atomic.Int32
	closeSends  atomic.Int32
}

type blockingDecryptStream struct {
	ctx         context.Context
	sendReturns atomic.Int32
}

func (f *blockingDecryptStream) Send(*pb.DecryptRequest) error {
	<-f.ctx.Done()
	f.sendReturns.Add(1)
	return f.ctx.Err()
}
func (f *blockingDecryptStream) Recv() (*pb.DecryptReply, error) {
	<-f.ctx.Done()
	return nil, f.ctx.Err()
}
func (f *blockingDecryptStream) Header() (metadata.MD, error) { return nil, nil }
func (f *blockingDecryptStream) Trailer() metadata.MD         { return nil }
func (f *blockingDecryptStream) CloseSend() error             { return nil }
func (f *blockingDecryptStream) Context() context.Context     { return f.ctx }
func (f *blockingDecryptStream) SendMsg(any) error            { return nil }
func (f *blockingDecryptStream) RecvMsg(any) error            { return nil }

type echoDecryptStream struct {
	ctx      context.Context
	requests chan *pb.DecryptRequest
}

func (f *echoDecryptStream) Send(req *pb.DecryptRequest) error {
	select {
	case f.requests <- req:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *echoDecryptStream) Recv() (*pb.DecryptReply, error) {
	select {
	case req := <-f.requests:
		return &pb.DecryptReply{Header: &pb.ReplyHeader{Code: 0}, Data: req.GetData()}, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}
func (f *echoDecryptStream) Header() (metadata.MD, error) { return nil, nil }
func (f *echoDecryptStream) Trailer() metadata.MD         { return nil }
func (f *echoDecryptStream) CloseSend() error             { return nil }
func (f *echoDecryptStream) Context() context.Context     { return f.ctx }
func (f *echoDecryptStream) SendMsg(any) error            { return nil }
func (f *echoDecryptStream) RecvMsg(any) error            { return nil }

func (f *fakeDecryptStream) Send(*pb.DecryptRequest) error {
	<-f.ctx.Done() // unblocks only once DecryptFragment cancels the session
	f.sendReturns.Add(1)
	return f.ctx.Err()
}
func (f *fakeDecryptStream) Recv() (*pb.DecryptReply, error) { return nil, f.recvErr }
func (f *fakeDecryptStream) Header() (metadata.MD, error)    { return nil, nil }
func (f *fakeDecryptStream) Trailer() metadata.MD            { return nil }
func (f *fakeDecryptStream) CloseSend() error                { f.closeSends.Add(1); return nil }
func (f *fakeDecryptStream) Context() context.Context        { return f.ctx }
func (f *fakeDecryptStream) SendMsg(any) error               { return nil }
func (f *fakeDecryptStream) RecvMsg(any) error               { return nil }

// TestDecryptFragmentWaitsForSenderOnRecvError verifies that when Recv fails
// while a Send is still in flight, DecryptFragment cancels the stream and waits
// for the sender to finish before returning — so the subsequent Close
// (CloseSend) cannot race a concurrent Send on the same stream.
func TestDecryptFragmentWaitsForSenderOnRecvError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeDecryptStream{ctx: ctx, recvErr: errors.New("recv boom")}
	session := &grpcDecryptSession{stream: fake, cancel: cancel, adamID: "adam"}

	done := make(chan error, 1)
	go func() {
		_, err := session.DecryptFragment("key", [][]byte{{1, 2, 3}})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("DecryptFragment returned nil, want the recv error")
		}
		// The sender must have been unblocked (cancelled) and drained before
		// return; otherwise a Send would still be running when Close is called.
		if got := fake.sendReturns.Load(); got != 1 {
			t.Fatalf("sender still in flight after return: sendReturns=%d, want 1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DecryptFragment hung; it did not cancel and drain the blocked sender")
	}

	// Close now runs CloseSend with no Send in flight — the race the fix closes.
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeSends.Load() != 1 {
		t.Fatalf("CloseSend called %d times, want 1", fake.closeSends.Load())
	}
}

func TestDecryptFragmentTimeoutCancelsAndDrainsStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &blockingDecryptStream{ctx: ctx}
	session := &grpcDecryptSession{
		stream: fake, cancel: cancel, adamID: "adam", fragmentTimeout: 20 * time.Millisecond,
	}

	started := time.Now()
	_, err := session.DecryptFragment("key", [][]byte{{1, 2, 3}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DecryptFragment error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("fragment timeout took %s, want prompt cancellation", elapsed)
	}
	if got := fake.sendReturns.Load(); got != 1 {
		t.Fatalf("sender returns = %d, want 1 before DecryptFragment returns", got)
	}
}

func TestDecryptTimeoutIsPerFragmentNotWholeSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &echoDecryptStream{ctx: ctx, requests: make(chan *pb.DecryptRequest)}
	timeout := 20 * time.Millisecond
	session := &grpcDecryptSession{
		stream: fake, cancel: cancel, adamID: "adam", fragmentTimeout: timeout,
	}

	first, err := session.DecryptFragment("key", [][]byte{{1}})
	if err != nil || len(first) != 1 || first[0][0] != 1 {
		t.Fatalf("first fragment = (%v, %v)", first, err)
	}
	// An old whole-session deadline would expire here. A fresh fragment gets
	// its own inactivity window and must still succeed.
	time.Sleep(2 * timeout)
	second, err := session.DecryptFragment("key", [][]byte{{2}})
	if err != nil || len(second) != 1 || second[0][0] != 2 {
		t.Fatalf("second fragment after idle gap = (%v, %v)", second, err)
	}
}
