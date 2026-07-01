package wrapper

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"amdl/internal/config"
	pb "github.com/AMDL-Web/wrapper-manager/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

type authTestServer struct {
	pb.UnimplementedWrapperManagerServiceServer
	status func(context.Context, *emptypb.Empty) (*pb.StatusReply, error)
	login  func(grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error
	logout func(context.Context, *pb.LogoutRequest) (*pb.LogoutReply, error)
	lyrics func(context.Context, *pb.LyricsRequest) (*pb.LyricsReply, error)
}

func (s *authTestServer) Status(ctx context.Context, req *emptypb.Empty) (*pb.StatusReply, error) {
	if s.status == nil {
		return nil, errors.New("unexpected status call")
	}
	return s.status(ctx, req)
}

func (s *authTestServer) Login(stream grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error {
	if s.login == nil {
		return errors.New("unexpected login call")
	}
	return s.login(stream)
}

func (s *authTestServer) Logout(ctx context.Context, req *pb.LogoutRequest) (*pb.LogoutReply, error) {
	if s.logout == nil {
		return nil, errors.New("unexpected logout call")
	}
	return s.logout(ctx, req)
}

func (s *authTestServer) Lyrics(ctx context.Context, req *pb.LyricsRequest) (*pb.LyricsReply, error) {
	if s.lyrics == nil {
		return nil, errors.New("unexpected lyrics call")
	}
	return s.lyrics(ctx, req)
}

func newAuthTestClient(t *testing.T, server *authTestServer, timeout time.Duration) *Client {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	pb.RegisterWrapperManagerServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &Client{
		conn:         conn,
		api:          pb.NewWrapperManagerServiceClient(conn),
		cfg:          config.WrapperConfig{TimeoutSeconds: 30},
		loginTimeout: timeout,
		sessions:     make(map[string]*loginSession),
	}
}

func successReply() *pb.LoginReply {
	return &pb.LoginReply{Header: &pb.ReplyHeader{Code: 0, Msg: "SUCCESS"}}
}

func TestAuthTimeoutUsesDedicatedWrapperSetting(t *testing.T) {
	client := &Client{cfg: config.WrapperConfig{TimeoutSeconds: 30, LoginTimeoutSeconds: 120}}
	if got := client.authTimeout(); got != 120*time.Second {
		t.Fatalf("auth timeout = %s, want 2m", got)
	}
}

func TestStatusReturnsAccountsWhenManagerProvidesThem(t *testing.T) {
	server := &authTestServer{status: func(context.Context, *emptypb.Empty) (*pb.StatusReply, error) {
		return &pb.StatusReply{
			Header: &pb.ReplyHeader{Code: 0, Msg: "SUCCESS"},
			Data: &pb.StatusData{
				Ready: true, Status: true, Regions: []string{"us"}, ClientCount: 1,
				Accounts: []string{"user@example.com"},
			},
		}, nil
	}}
	client := newAuthTestClient(t, server, time.Second)

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.AccountsSupported || len(status.Accounts) != 1 || status.Accounts[0] != "user@example.com" {
		t.Fatalf("unexpected accounts status: %#v", status)
	}
}

func TestStartLoginSucceedsWithoutTwoStep(t *testing.T) {
	server := &authTestServer{login: func(stream grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		if req.GetData().GetUsername() != "user" || req.GetData().GetPassword() != "secret" {
			t.Fatalf("unexpected login request: %#v", req.GetData())
		}
		return stream.Send(successReply())
	}}
	client := newAuthTestClient(t, server, time.Second)

	result, err := client.StartLogin(context.Background(), "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != LoginStatusLoggedIn || result.LoginID != "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLoginContinuesOnSameStreamForTwoStep(t *testing.T) {
	server := &authTestServer{login: func(stream grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error {
		first, err := stream.Recv()
		if err != nil {
			return err
		}
		if first.GetData().GetUsername() != "user" {
			t.Fatalf("unexpected username %q", first.GetData().GetUsername())
		}
		if err := stream.Send(&pb.LoginReply{Header: &pb.ReplyHeader{Code: 2, Msg: "2fa code require"}}); err != nil {
			return err
		}
		second, err := stream.Recv()
		if err != nil {
			return err
		}
		if second.GetData().GetTwoStepCode() != "123456" {
			t.Fatalf("unexpected code %q", second.GetData().GetTwoStepCode())
		}
		if second.GetData().GetUsername() != "user" {
			t.Fatalf("two-step username = %q, want user", second.GetData().GetUsername())
		}
		return stream.Send(successReply())
	}}
	client := newAuthTestClient(t, server, time.Second)

	started, err := client.StartLogin(context.Background(), "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != LoginStatusNeedsTwoStep || started.LoginID == "" {
		t.Fatalf("unexpected start result: %#v", started)
	}
	completed, err := client.SubmitTwoStepCode(context.Background(), started.LoginID, "123456")
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != LoginStatusLoggedIn {
		t.Fatalf("unexpected completion: %#v", completed)
	}
	if _, err := client.SubmitTwoStepCode(context.Background(), started.LoginID, "123456"); !errors.Is(err, ErrLoginSessionNotFound) {
		t.Fatalf("second submission error = %v", err)
	}
}

func TestSubmitTwoStepCodeRejectsConcurrentSubmission(t *testing.T) {
	release := make(chan struct{})
	server := &authTestServer{login: func(stream grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		if err := stream.Send(&pb.LoginReply{Header: &pb.ReplyHeader{Code: 2}}); err != nil {
			return err
		}
		if _, err := stream.Recv(); err != nil {
			return err
		}
		<-release
		return stream.Send(successReply())
	}}
	client := newAuthTestClient(t, server, time.Second)
	started, err := client.StartLogin(context.Background(), "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := client.SubmitTwoStepCode(context.Background(), started.LoginID, "123456")
		firstDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if _, err := client.SubmitTwoStepCode(context.Background(), started.LoginID, "123456"); !errors.Is(err, ErrLoginSessionBusy) {
		t.Fatalf("concurrent submission error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestStartLoginMapsAuthenticationAndConflictErrors(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want error
	}{
		{name: "failed", msg: "login failed", want: ErrAuthenticationFailed},
		{name: "already logged in", msg: "already login", want: ErrAlreadyLoggedIn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &authTestServer{login: func(stream grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error {
				if _, err := stream.Recv(); err != nil {
					return err
				}
				return stream.Send(&pb.LoginReply{Header: &pb.ReplyHeader{Code: -1, Msg: tt.msg}})
			}}
			client := newAuthTestClient(t, server, time.Second)
			_, err := client.StartLogin(context.Background(), "user", "secret")
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestTwoStepSessionExpires(t *testing.T) {
	server := &authTestServer{login: func(stream grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		if err := stream.Send(&pb.LoginReply{Header: &pb.ReplyHeader{Code: 2}}); err != nil {
			return err
		}
		_, err := stream.Recv()
		if errors.Is(err, context.Canceled) || err == io.EOF {
			return nil
		}
		return err
	}}
	client := newAuthTestClient(t, server, 30*time.Millisecond)
	started, err := client.StartLogin(context.Background(), "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := client.SubmitTwoStepCode(context.Background(), started.LoginID, "123456"); !errors.Is(err, ErrLoginSessionNotFound) {
		t.Fatalf("expired session error = %v", err)
	}
}

func TestTwoStepVerificationFailsAndCleansSession(t *testing.T) {
	server := &authTestServer{login: func(stream grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		if err := stream.Send(&pb.LoginReply{Header: &pb.ReplyHeader{Code: 2}}); err != nil {
			return err
		}
		if _, err := stream.Recv(); err != nil {
			return err
		}
		return stream.Send(&pb.LoginReply{Header: &pb.ReplyHeader{Code: -1, Msg: "login failed"}})
	}}
	client := newAuthTestClient(t, server, time.Second)
	started, err := client.StartLogin(context.Background(), "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SubmitTwoStepCode(context.Background(), started.LoginID, "bad"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("verification error = %v", err)
	}
	if _, err := client.SubmitTwoStepCode(context.Background(), started.LoginID, "bad"); !errors.Is(err, ErrLoginSessionNotFound) {
		t.Fatalf("cleaned session error = %v", err)
	}
}

func TestTwoStepVerificationUsesRemainingSessionTimeout(t *testing.T) {
	server := &authTestServer{login: func(stream grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		if err := stream.Send(&pb.LoginReply{Header: &pb.ReplyHeader{Code: 2}}); err != nil {
			return err
		}
		if _, err := stream.Recv(); err != nil {
			return err
		}
		<-stream.Context().Done()
		return nil
	}}
	client := newAuthTestClient(t, server, 80*time.Millisecond)
	started, err := client.StartLogin(context.Background(), "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	startedAt := time.Now()
	_, err = client.SubmitTwoStepCode(context.Background(), started.LoginID, "123456")
	if !errors.Is(err, ErrLoginTimeout) {
		t.Fatalf("verification error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= 70*time.Millisecond {
		t.Fatalf("verification reset session timeout: elapsed %s", elapsed)
	}
}

func TestStartLoginTimesOut(t *testing.T) {
	server := &authTestServer{login: func(stream grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		<-stream.Context().Done()
		return nil
	}}
	client := newAuthTestClient(t, server, 30*time.Millisecond)
	_, err := client.StartLogin(context.Background(), "user", "secret")
	if !errors.Is(err, ErrLoginTimeout) {
		t.Fatalf("error = %v, want timeout", err)
	}
}

func TestLogout(t *testing.T) {
	tests := []struct {
		name string
		code int32
		msg  string
		want error
	}{
		{name: "success"},
		{name: "missing", code: -1, msg: "no such account", want: ErrAccountNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &authTestServer{
				login: func(grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error { return nil },
				logout: func(_ context.Context, req *pb.LogoutRequest) (*pb.LogoutReply, error) {
					if req.GetData().GetUsername() != "user" {
						t.Fatalf("unexpected username %q", req.GetData().GetUsername())
					}
					return &pb.LogoutReply{Header: &pb.ReplyHeader{Code: tt.code, Msg: tt.msg}}, nil
				},
			}
			client := newAuthTestClient(t, server, time.Second)
			err := client.Logout(context.Background(), "user")
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestLyricsSendsTypeAndLocalizationOptions(t *testing.T) {
	server := &authTestServer{
		login:  func(grpc.BidiStreamingServer[pb.LoginRequest, pb.LoginReply]) error { return nil },
		logout: func(context.Context, *pb.LogoutRequest) (*pb.LogoutReply, error) { return nil, nil },
		lyrics: func(_ context.Context, req *pb.LyricsRequest) (*pb.LyricsReply, error) {
			data := req.GetData()
			if data.GetAdamId() != "123" {
				t.Fatalf("adam id = %q, want 123", data.GetAdamId())
			}
			if data.GetRegion() != "jp" {
				t.Fatalf("region = %q, want jp", data.GetRegion())
			}
			if data.GetLanguage() != "ja-JP" {
				t.Fatalf("language = %q, want ja-JP", data.GetLanguage())
			}
			if data.GetType() != "syllable-lyrics" {
				t.Fatalf("type = %q, want syllable-lyrics", data.GetType())
			}
			if !data.GetExtendTtmlLocalizations() {
				t.Fatal("extend_ttml_localizations = false, want true")
			}
			return &pb.LyricsReply{
				Header: &pb.ReplyHeader{Code: 0},
				Data:   &pb.LyricsDataResponse{AdamId: "123", Lyrics: "<tt/>"},
			}, nil
		},
	}
	client := newAuthTestClient(t, server, time.Second)

	got, err := client.Lyrics(context.Background(), "123", LyricsRequestOptions{
		Region:                  "jp",
		Language:                "ja-JP",
		Type:                    "syllable-lyrics",
		ExtendTtmlLocalizations: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "<tt/>" {
		t.Fatalf("lyrics = %q, want <tt/>", got)
	}
}
