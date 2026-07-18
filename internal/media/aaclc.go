package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"

	"amdl/internal/limits"

	widevine "github.com/iyear/gowidevine"
	wvpb "github.com/iyear/gowidevine/widevinepb"
	"google.golang.org/protobuf/proto"
)

type aacLCMedia struct {
	MediaURI string
	KeyURI   string
	KID      string
}

func extractAACLCMedia(ctx context.Context, client *http.Client, playlistURL string, gates ...*limits.RequestGate) (aacLCMedia, error) {
	body, err := downloadText(ctx, client, playlistURL, gates...)
	if err != nil {
		return aacLCMedia{}, err
	}
	var media aacLCMedia
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "#EXT-X-KEY:") {
			keyURI := parseAttrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))["URI"]
			if keyURI != "" {
				media.KeyURI = keyURI
				parts := strings.SplitN(keyURI, ",", 2)
				if len(parts) == 2 {
					media.KID = parts[1]
				}
			}
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			mapURI := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))["URI"]
			if mapURI != "" {
				media.MediaURI = absURL(playlistURL, mapURI)
			}
		}
	}
	if media.MediaURI == "" {
		return aacLCMedia{}, fmt.Errorf("aac-lc playlist has no EXT-X-MAP media URI")
	}
	if media.KeyURI == "" || media.KID == "" {
		return aacLCMedia{}, fmt.Errorf("aac-lc playlist has no Widevine key URI")
	}
	if _, err := base64.StdEncoding.DecodeString(media.KID); err != nil {
		return aacLCMedia{}, fmt.Errorf("decode aac-lc KID: %w", err)
	}
	return media, nil
}

func newWidevineSession(kidBase64 string) ([]byte, func([]byte) ([]*widevine.Key, error), error) {
	deviceRaw, err := base64.StdEncoding.DecodeString(widevineDeviceBase64)
	if err != nil {
		return nil, nil, fmt.Errorf("decode embedded Widevine device: %w", err)
	}
	device, err := widevine.NewDevice(widevine.FromWVD(bytes.NewReader(deviceRaw)))
	if err != nil {
		return nil, nil, fmt.Errorf("load Widevine device: %w", err)
	}
	pssh, err := makeWidevinePSSH(kidBase64)
	if err != nil {
		return nil, nil, err
	}
	challenge, parseLicense, err := widevine.NewCDM(device).GetLicenseChallenge(pssh, wvpb.LicenseType_AUTOMATIC, false)
	if err != nil {
		return nil, nil, fmt.Errorf("generate Widevine license challenge: %w", err)
	}
	return challenge, parseLicense, nil
}

func makeWidevinePSSH(kidBase64 string) (*widevine.PSSH, error) {
	kid, err := base64.StdEncoding.DecodeString(kidBase64)
	if err != nil {
		return nil, fmt.Errorf("decode Widevine KID: %w", err)
	}
	data, err := proto.Marshal(&wvpb.WidevinePsshData{KeyIds: [][]byte{kid}})
	if err != nil {
		return nil, fmt.Errorf("marshal Widevine PSSH data: %w", err)
	}
	systemID := []byte{0xed, 0xef, 0x8b, 0xa9, 0x79, 0xd6, 0x4a, 0xce, 0xa3, 0xc8, 0x27, 0xdc, 0xd5, 0x1d, 0x21, 0xed}
	var box bytes.Buffer
	_ = binary.Write(&box, binary.BigEndian, uint32(32+len(data)))
	box.WriteString("pssh")
	_ = binary.Write(&box, binary.BigEndian, uint32(0))
	box.Write(systemID)
	_ = binary.Write(&box, binary.BigEndian, uint32(len(data)))
	box.Write(data)
	pssh, err := widevine.NewPSSH(box.Bytes())
	if err != nil {
		return nil, fmt.Errorf("parse generated Widevine PSSH: %w", err)
	}
	return pssh, nil
}

func decodeWidevineLicense(value string) ([]byte, error) {
	license, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode Widevine license: %w", err)
	}
	return license, nil
}

func decryptWidevineMP4(raw []byte, licenseValue string, parseLicense func([]byte) ([]*widevine.Key, error)) ([]byte, error) {
	license, err := decodeWidevineLicense(licenseValue)
	if err != nil {
		return nil, err
	}
	keys, err := parseLicense(license)
	if err != nil {
		return nil, fmt.Errorf("parse Widevine license: %w", err)
	}
	var decrypted bytes.Buffer
	if err := widevine.DecryptMP4Auto(bytes.NewReader(raw), keys, &decrypted); err != nil {
		return nil, fmt.Errorf("decrypt AAC-LC MP4: %w", err)
	}
	return decrypted.Bytes(), nil
}
