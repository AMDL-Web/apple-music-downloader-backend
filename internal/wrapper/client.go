package wrapper

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"amdl/internal/config"
	pb "github.com/AMDL-Web/wrapper-manager/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

type Client struct {
	conn         *grpc.ClientConn
	api          pb.WrapperManagerServiceClient
	cfg          config.WrapperConfig
	loginTimeout time.Duration
	sessionsMu   sync.Mutex
	sessions     map[string]*loginSession
}

var (
	ErrAuthenticationFailed = errors.New("wrapper authentication failed")
	ErrAlreadyLoggedIn      = errors.New("wrapper account already logged in")
	ErrLoginSessionNotFound = errors.New("wrapper login session not found")
	ErrLoginSessionBusy     = errors.New("wrapper login session is already being verified")
	ErrLoginTimeout         = errors.New("wrapper login timed out")
	ErrAccountNotFound      = errors.New("wrapper account not found")
	ErrWrapperResponse      = errors.New("wrapper returned an unexpected response")
)

const (
	LoginStatusLoggedIn     = "logged_in"
	LoginStatusNeedsTwoStep = "needs_2fa"
)

type LoginResult struct {
	Status  string `json:"status"`
	LoginID string `json:"login_id,omitempty"`
}

type loginSession struct {
	stream    pb.WrapperManagerService_LoginClient
	cancel    context.CancelFunc
	username  string
	expiresAt time.Time
	timer     *time.Timer
	busy      bool
}

type Status struct {
	Ready             bool     `json:"ready"`
	Status            bool     `json:"status"`
	Regions           []string `json:"regions"`
	ClientCount       int32    `json:"client_count"`
	Accounts          []string `json:"accounts,omitempty"`
	AccountsSupported bool     `json:"accounts_supported"`
}

// DecryptSession is a live decrypt stream to the wrapper for a single track.
// Samples are decrypted in fragment-sized batches so a whole track's plaintext
// never has to be materialised in memory at once.
type DecryptSession interface {
	// DecryptFragment sends one fragment's still-encrypted samples (all of which
	// share key) and returns their plaintext in the same order. It blocks until
	// every sample in the batch has come back.
	DecryptFragment(key string, samples [][]byte) ([][]byte, error)
	// Close finishes the send side of the stream and releases its resources.
	Close() error
}

type LyricsRequestOptions struct {
	Region                  string
	Language                string
	Type                    string
	ExtendTtmlLocalizations bool
}

func NewClient(cfg config.WrapperConfig) (*Client, error) {
	opts := []grpc.DialOption{grpc.WithTransportCredentials(wrapperTransportCredentials(cfg))}
	conn, err := grpc.NewClient(cfg.Address, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, api: pb.NewWrapperManagerServiceClient(conn), cfg: cfg, sessions: make(map[string]*loginSession)}, nil
}

func wrapperTransportCredentials(cfg config.WrapperConfig) credentials.TransportCredentials {
	if cfg.Insecure {
		return insecure.NewCredentials()
	}
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
}

func (c *Client) Close() error {
	c.sessionsMu.Lock()
	for id, session := range c.sessions {
		if session.timer != nil {
			session.timer.Stop()
		}
		session.cancel()
		delete(c.sessions, id)
	}
	c.sessionsMu.Unlock()
	return c.conn.Close()
}

func (c *Client) authTimeout() time.Duration {
	if c.loginTimeout > 0 {
		return c.loginTimeout
	}
	return c.cfg.LoginTimeout()
}

func (c *Client) StartLogin(ctx context.Context, username, password string) (LoginResult, error) {
	streamCtx, cancel := context.WithCancel(context.Background())
	stream, err := c.api.Login(streamCtx)
	if err != nil {
		cancel()
		return LoginResult{}, err
	}
	if err := stream.Send(&pb.LoginRequest{Data: &pb.LoginData{Username: username, Password: password}}); err != nil {
		cancel()
		return LoginResult{}, err
	}
	reply, err := receiveLoginReply(ctx, stream, c.authTimeout())
	if err != nil {
		cancel()
		return LoginResult{}, err
	}
	switch reply.GetHeader().GetCode() {
	case 0:
		cancel()
		return LoginResult{Status: LoginStatusLoggedIn}, nil
	case 2:
		id, err := randomLoginID()
		if err != nil {
			cancel()
			return LoginResult{}, err
		}
		timeout := c.authTimeout()
		session := &loginSession{stream: stream, cancel: cancel, username: username, expiresAt: time.Now().Add(timeout)}
		c.sessionsMu.Lock()
		c.sessions[id] = session
		session.timer = time.AfterFunc(timeout, func() { c.expireSession(id, session) })
		c.sessionsMu.Unlock()
		return LoginResult{Status: LoginStatusNeedsTwoStep, LoginID: id}, nil
	case -1:
		cancel()
		if strings.Contains(strings.ToLower(reply.GetHeader().GetMsg()), "already login") {
			return LoginResult{}, ErrAlreadyLoggedIn
		}
		return LoginResult{}, ErrAuthenticationFailed
	default:
		cancel()
		return LoginResult{}, fmt.Errorf("%w: login code %d: %s", ErrWrapperResponse, reply.GetHeader().GetCode(), reply.GetHeader().GetMsg())
	}
}

func (c *Client) SubmitTwoStepCode(ctx context.Context, loginID, code string) (LoginResult, error) {
	session, remaining, err := c.acquireSession(loginID)
	if err != nil {
		return LoginResult{}, err
	}
	defer c.removeSession(loginID, session)
	if err := session.stream.Send(&pb.LoginRequest{Data: &pb.LoginData{Username: session.username, TwoStepCode: code}}); err != nil {
		return LoginResult{}, err
	}
	reply, err := receiveLoginReply(ctx, session.stream, remaining)
	if err != nil {
		return LoginResult{}, err
	}
	switch reply.GetHeader().GetCode() {
	case 0:
		return LoginResult{Status: LoginStatusLoggedIn}, nil
	case -1:
		return LoginResult{}, ErrAuthenticationFailed
	default:
		return LoginResult{}, fmt.Errorf("%w: two-step code %d: %s", ErrWrapperResponse, reply.GetHeader().GetCode(), reply.GetHeader().GetMsg())
	}
}

func (c *Client) Logout(ctx context.Context, username string) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()
	reply, err := c.api.Logout(ctx, &pb.LogoutRequest{Data: &pb.LogoutData{Username: username}})
	if err != nil {
		return err
	}
	if reply.GetHeader().GetCode() == 0 {
		return nil
	}
	if strings.Contains(strings.ToLower(reply.GetHeader().GetMsg()), "no such account") {
		return ErrAccountNotFound
	}
	return fmt.Errorf("%w: logout code %d: %s", ErrWrapperResponse, reply.GetHeader().GetCode(), reply.GetHeader().GetMsg())
}

func receiveLoginReply(ctx context.Context, stream pb.WrapperManagerService_LoginClient, timeout time.Duration) (*pb.LoginReply, error) {
	type result struct {
		reply *pb.LoginReply
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		reply, err := stream.Recv()
		resultCh <- result{reply: reply, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrLoginTimeout
	case result := <-resultCh:
		return result.reply, result.err
	}
}

func randomLoginID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate login id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (c *Client) acquireSession(id string) (*loginSession, time.Duration, error) {
	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	session, ok := c.sessions[id]
	if !ok || time.Now().After(session.expiresAt) {
		return nil, 0, ErrLoginSessionNotFound
	}
	if session.busy {
		return nil, 0, ErrLoginSessionBusy
	}
	session.busy = true
	if session.timer != nil {
		session.timer.Stop()
	}
	return session, time.Until(session.expiresAt), nil
}

func (c *Client) expireSession(id string, expected *loginSession) {
	c.removeSession(id, expected)
}

func (c *Client) removeSession(id string, expected *loginSession) {
	c.sessionsMu.Lock()
	session, ok := c.sessions[id]
	if ok && session == expected {
		delete(c.sessions, id)
		if session.timer != nil {
			session.timer.Stop()
		}
		session.cancel()
	}
	c.sessionsMu.Unlock()
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()
	resp, err := c.api.Status(ctx, &emptypb.Empty{})
	if err != nil {
		return Status{}, err
	}
	if resp.GetHeader().GetCode() != 0 {
		return Status{}, fmt.Errorf("wrapper status: %s", resp.GetHeader().GetMsg())
	}
	data := resp.GetData()
	return Status{
		Ready:             data.GetReady(),
		Status:            data.GetStatus(),
		Regions:           data.GetRegions(),
		ClientCount:       data.GetClientCount(),
		Accounts:          data.GetAccounts(),
		AccountsSupported: statusAccountsSupported(data),
	}, nil
}

func statusAccountsSupported(data *pb.StatusData) bool {
	if data == nil {
		return false
	}
	field := data.ProtoReflect().Descriptor().Fields().ByName("accounts")
	return field != nil && data.ProtoReflect().Get(field).List().Len() > 0
}

func (c *Client) M3U8(ctx context.Context, adamID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()
	resp, err := c.api.M3U8(ctx, &pb.M3U8Request{Data: &pb.M3U8DataRequest{AdamId: adamID}})
	if err != nil {
		return "", err
	}
	if resp.GetHeader().GetCode() != 0 {
		return "", fmt.Errorf("wrapper m3u8: %s", resp.GetHeader().GetMsg())
	}
	return resp.GetData().GetM3U8(), nil
}

func (c *Client) Lyrics(ctx context.Context, adamID string, opts LyricsRequestOptions) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()
	resp, err := c.api.Lyrics(ctx, &pb.LyricsRequest{Data: &pb.LyricsDataRequest{
		AdamId: adamID, Region: opts.Region, Language: opts.Language,
		Type: opts.Type, ExtendTtmlLocalizations: opts.ExtendTtmlLocalizations,
	}})
	if err != nil {
		return "", err
	}
	if resp.GetHeader().GetCode() != 0 {
		return "", fmt.Errorf("wrapper lyrics: %s", resp.GetHeader().GetMsg())
	}
	return resp.GetData().GetLyrics(), nil
}

func (c *Client) WebPlayback(ctx context.Context, adamID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()
	resp, err := c.api.WebPlayback(ctx, &pb.WebPlaybackRequest{Data: &pb.WebPlaybackDataRequest{AdamId: adamID}})
	if err != nil {
		return "", err
	}
	if resp.GetHeader().GetCode() != 0 {
		return "", fmt.Errorf("wrapper web playback: %s", resp.GetHeader().GetMsg())
	}
	return resp.GetData().GetM3U8(), nil
}

func (c *Client) License(ctx context.Context, adamID, challenge, uri string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()
	resp, err := c.api.License(ctx, &pb.LicenseRequest{Data: &pb.LicenseDataRequest{
		AdamId: adamID, Challenge: challenge, Uri: uri,
	}})
	if err != nil {
		return "", err
	}
	if resp.GetHeader().GetCode() != 0 {
		return "", fmt.Errorf("wrapper license: %s", resp.GetHeader().GetMsg())
	}
	return resp.GetData().GetLicense(), nil
}

// NewDecryptSession opens a bidirectional decrypt stream for one track. The
// stream is fed one fragment at a time (DecryptFragment) and torn down with
// Close, so the caller can decrypt a whole track without ever holding all of
// its samples in memory. A track may legitimately take much longer than a
// unary RPC, so the configured timeout bounds each fragment operation rather
// than the lifetime of the whole stream.
func (c *Client) NewDecryptSession(ctx context.Context, adamID string) (DecryptSession, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := c.api.Decrypt(streamCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	return &grpcDecryptSession{stream: stream, cancel: cancel, adamID: adamID, fragmentTimeout: c.cfg.Timeout()}, nil
}

type grpcDecryptSession struct {
	stream pb.WrapperManagerService_DecryptClient
	cancel context.CancelFunc
	adamID string
	// fragmentTimeout is an inactivity bound for one fragment request/reply
	// batch. It deliberately is not a whole-track deadline.
	fragmentTimeout time.Duration
	// next is the monotonically increasing sample index used to correlate
	// requests with their replies. It spans the whole track (not reset per
	// fragment) so indices stay unique across every batch on the one stream.
	next int
}

func (s *grpcDecryptSession) DecryptFragment(key string, samples [][]byte) ([][]byte, error) {
	if len(samples) == 0 {
		return nil, nil
	}
	if s.fragmentTimeout <= 0 {
		return s.decryptFragment(key, samples)
	}
	type result struct {
		out [][]byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := s.decryptFragment(key, samples)
		done <- result{out: out, err: err}
	}()
	timer := time.NewTimer(s.fragmentTimeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.out, result.err
	case <-timer.C:
		// Cancelling the stream unblocks both Send and Recv. Wait for the
		// operation to unwind so CloseSend cannot race a lingering Send.
		s.cancel()
		<-done
		return nil, fmt.Errorf("wrapper decrypt fragment timed out after %s: %w", s.fragmentTimeout, context.DeadlineExceeded)
	}
}

func (s *grpcDecryptSession) decryptFragment(key string, samples [][]byte) ([][]byte, error) {
	n := len(samples)
	base := s.next
	s.next += n
	// Send in the background while receiving: a fragment can carry more sample
	// bytes than the gRPC flow-control window, so sending everything before
	// reading the first reply could deadlock.
	sendErr := make(chan error, 1)
	go func() {
		for i, data := range samples {
			if err := s.stream.Send(&pb.DecryptRequest{Data: &pb.DecryptData{
				AdamId: s.adamID, Key: key, SampleIndex: int32(base + i), Sample: data,
			}}); err != nil {
				sendErr <- err
				return
			}
		}
		sendErr <- nil
	}()

	out := make([][]byte, n)
	recvErr := s.receiveBatch(out, base)
	if recvErr != nil {
		// The receive failed partway, so the sender may still be blocked on Send.
		// Cancel the stream to unblock it and wait for it to exit before
		// returning: the caller runs Close (CloseSend) next, which must not race
		// a concurrent Send on this stream. The session is unusable after an
		// error anyway, so cancelling here is fine.
		s.cancel()
		<-sendErr
		return nil, recvErr
	}
	if err := <-sendErr; err != nil {
		return nil, err
	}
	return out, nil
}

// receiveBatch reads exactly len(out) decrypt replies into out, correlating each
// reply's sample index against base.
func (s *grpcDecryptSession) receiveBatch(out [][]byte, base int) error {
	for received := 0; received < len(out); received++ {
		resp, err := s.stream.Recv()
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("wrapper decrypt stream ended after %d/%d samples", received, len(out))
			}
			return err
		}
		if resp.GetHeader().GetCode() != 0 {
			return fmt.Errorf("wrapper decrypt: %s", resp.GetHeader().GetMsg())
		}
		idx := int(resp.GetData().GetSampleIndex()) - base
		if idx < 0 || idx >= len(out) {
			return fmt.Errorf("wrapper decrypt returned out-of-range sample index %d", resp.GetData().GetSampleIndex())
		}
		out[idx] = resp.GetData().GetSample()
	}
	return nil
}

func (s *grpcDecryptSession) Close() error {
	err := s.stream.CloseSend()
	s.cancel()
	return err
}
