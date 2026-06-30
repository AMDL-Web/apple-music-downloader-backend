package wrapper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"amdl/backend/internal/config"
	pb "amdl/backend/internal/wrapperproto"
	"google.golang.org/grpc"
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
	Ready       bool     `json:"ready"`
	Status      bool     `json:"status"`
	Regions     []string `json:"regions"`
	ClientCount int32    `json:"client_count"`
}

type DecryptSample struct {
	Key   string
	Index int
	Data  []byte
}

func NewClient(cfg config.WrapperConfig) (*Client, error) {
	opts := []grpc.DialOption{}
	if cfg.Insecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.NewClient(cfg.Address, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, api: pb.NewWrapperManagerServiceClient(conn), cfg: cfg, sessions: make(map[string]*loginSession)}, nil
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
	return Status{Ready: data.GetReady(), Status: data.GetStatus(), Regions: data.GetRegions(), ClientCount: data.GetClientCount()}, nil
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

func (c *Client) Lyrics(ctx context.Context, adamID, region, language string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()
	resp, err := c.api.Lyrics(ctx, &pb.LyricsRequest{Data: &pb.LyricsDataRequest{AdamId: adamID, Region: region, Language: language}})
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

// Decrypt sends all samples to the wrapper for decryption and returns them in
// order. onSample, if non-nil, is called after each sample is received with
// (receivedCount, totalCount) so callers can track decryption progress.
func (c *Client) Decrypt(ctx context.Context, adamID string, samples []DecryptSample, onSample func(received, total int)) ([][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()
	stream, err := c.api.Decrypt(ctx)
	if err != nil {
		return nil, err
	}
	sendErr := make(chan error, 1)
	go func() {
		for _, sample := range samples {
			err := stream.Send(&pb.DecryptRequest{Data: &pb.DecryptData{
				AdamId: adamID, Key: sample.Key, SampleIndex: int32(sample.Index), Sample: sample.Data,
			}})
			if err != nil {
				sendErr <- err
				return
			}
		}
		sendErr <- stream.CloseSend()
	}()

	out := make([][]byte, len(samples))
	received := 0
	for received < len(samples) {
		resp, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if resp.GetHeader().GetCode() != 0 {
			return nil, fmt.Errorf("wrapper decrypt: %s", resp.GetHeader().GetMsg())
		}
		idx := int(resp.GetData().GetSampleIndex())
		if idx < 0 || idx >= len(out) {
			return nil, fmt.Errorf("wrapper decrypt returned invalid sample index %d", idx)
		}
		out[idx] = resp.GetData().GetSample()
		received++
		if onSample != nil {
			onSample(received, len(samples))
		}
	}
	if err := <-sendErr; err != nil {
		return nil, err
	}
	if received != len(samples) {
		return nil, fmt.Errorf("wrapper decrypt returned %d/%d samples", received, len(samples))
	}
	return out, nil
}
