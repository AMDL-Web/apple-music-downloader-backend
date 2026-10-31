package media

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/wrapper"
)

// simulatedMedia is the simulate-mode backend. Catalog metadata and enhanced
// media selection run for real before it is reached; everything it is asked to
// do it only pretends, pacing transfers by a speed drawn from the configured
// simulate range. It never touches the wrapper, ffmpeg or the disk (it only
// reads whether an output already exists), so test mode needs neither a
// decryptor nor ffmpeg, and never deletes a real download even under force.
type simulatedMedia struct{ cfg config.Config }

// simulatedLyricsTTML stands in for the wrapper's lyrics document, so the
// lyrics conversion and lyrics_status behave as in a real download.
const simulatedLyricsTTML = `<tt><body><div><p begin="00:00.000">Simulated lyrics</p></div></body></tt>`

// Pauses for the steps that have no byte count. They only need to be long
// enough for a client to see each state.
const (
	simulatedKeyPause    = 100 * time.Millisecond
	simulatedAACLCPause  = 200 * time.Millisecond
	simulatedRemuxPause  = 200 * time.Millisecond
	simulatedVerifyPause = 100 * time.Millisecond
	simulatedTagPause    = 150 * time.Millisecond
)

func (simulatedMedia) coverArt(context.Context, []string, string, string) ([]byte, error) {
	return nil, nil
}

func (simulatedMedia) lyrics(context.Context, string, wrapper.LyricsRequestOptions) (string, error) {
	return simulatedLyricsTTML, nil
}

func (simulatedMedia) requireTools(context.Context) error { return nil }

func (simulatedMedia) resolveAACLC(context.Context, string) (aacLCMedia, error) {
	return aacLCMedia{}, nil
}

func (m simulatedMedia) fetch(ctx context.Context, req fetchRequest, progress transferProgress) (encryptedMedia, error) {
	size := req.estimatedBytes
	if size <= 0 {
		size = estimatedMediaBytes(0, 0)
	}
	if err := m.transfer(ctx, size, func(p float64) { progress(int64(p*float64(size)), size) }); err != nil {
		return encryptedMedia{}, err
	}
	return encryptedMedia{size: size}, nil
}

func (simulatedMedia) discardCheckpoint(string, string) {}

func (simulatedMedia) discardOutput(string) {}

func (simulatedMedia) stage(media encryptedMedia) (stagedFile, func(), error) {
	return stagedFile{size: media.size}, func() {}, nil
}

// prepareKeys has no wrapper to ask; the simulated decrypt pauses for its key
// instead.
func (simulatedMedia) prepareKeys(context.Context, string, []string) error { return nil }

func (m simulatedMedia) decrypt(ctx context.Context, req decryptRequest, hooks decryptHooks) error {
	if err := simulatePause(ctx, simulatedKeyPause); err != nil {
		return err
	}
	hooks.ready()
	if req.codec == "aac-lc" {
		if err := simulatePause(ctx, simulatedAACLCPause); err != nil {
			return err
		}
	} else {
		// Decrypt works on bytes already held, so pace it faster than the
		// network transfer: the same speed range over a third of the size.
		progress := hooks.progress
		if progress == nil {
			progress = func(float64) {}
		}
		if err := m.transfer(ctx, req.size/3, progress); err != nil {
			return err
		}
	}
	hooks.afterInput()
	return simulatePause(ctx, simulatedRemuxPause)
}

func (simulatedMedia) verify(ctx context.Context, _ stagedFile) error {
	return simulatePause(ctx, simulatedVerifyPause)
}

// sampleRate has no file to read: a simulated AAC item keeps the rate its
// manifest named, if any.
func (simulatedMedia) sampleRate(stagedFile) (int, error) { return 0, nil }

func (simulatedMedia) repairALAC(context.Context, stagedFile, func(int)) error {
	// verify never fails here, so the pipeline never asks.
	return errors.New("simulated media has nothing to repair")
}

func (simulatedMedia) mkdirAll(string) error { return nil }

func (simulatedMedia) writeFile(string, []byte) error { return nil }

func (simulatedMedia) tag(ctx context.Context, _ stagedFile, _ applemusic.Song, _ string, _ []byte) error {
	return simulatePause(ctx, simulatedTagPause)
}

func (simulatedMedia) commit(staged stagedFile, _ string) (int64, error) {
	return staged.size, nil
}

// transfer advances a fake transfer of totalBytes, re-rolling the speed inside
// [min_speed_kbps, max_speed_kbps] every tick and reporting completion in
// [0,1] through progress.
func (m simulatedMedia) transfer(ctx context.Context, totalBytes int64, progress func(float64)) error {
	if totalBytes <= 0 {
		totalBytes = 1
	}
	minSpeed := max(m.cfg.Simulate.MinSpeedKBps, 1)
	maxSpeed := max(m.cfg.Simulate.MaxSpeedKBps, minSpeed)
	const tick = 200 * time.Millisecond
	var done float64
	for done < float64(totalBytes) {
		if err := simulatePause(ctx, tick); err != nil {
			return err
		}
		speed := float64(minSpeed)
		if maxSpeed > minSpeed {
			speed += rand.Float64() * float64(maxSpeed-minSpeed)
		}
		done += speed * 1024 * tick.Seconds()
		progress(min(1, done/float64(totalBytes)))
	}
	return nil
}

func simulatePause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
