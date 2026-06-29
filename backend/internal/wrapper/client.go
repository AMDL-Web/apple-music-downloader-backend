package wrapper

import (
	"context"
	"fmt"
	"io"

	"amdl/backend/internal/config"
	pb "amdl/backend/internal/wrapperproto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

type Client struct {
	conn *grpc.ClientConn
	api  pb.WrapperManagerServiceClient
	cfg  config.WrapperConfig
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
	return &Client{conn: conn, api: pb.NewWrapperManagerServiceClient(conn), cfg: cfg}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

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

func (c *Client) Decrypt(ctx context.Context, adamID string, samples []DecryptSample) ([][]byte, error) {
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
	}
	if err := <-sendErr; err != nil {
		return nil, err
	}
	if received != len(samples) {
		return nil, fmt.Errorf("wrapper decrypt returned %d/%d samples", received, len(samples))
	}
	return out, nil
}
