package media

import (
	"context"
	"os/exec"

	"amdl/backend/internal/config"
	"amdl/backend/internal/domain"
)

type ToolChecker struct {
	cfg config.ToolsConfig
}

func NewToolChecker(cfg config.ToolsConfig) *ToolChecker {
	return &ToolChecker{cfg: cfg}
}

func (c *ToolChecker) Check(ctx context.Context) []domain.Capability {
	tools := map[string]string{
		"ffmpeg":     c.cfg.FFmpeg,
		"gpac":       c.cfg.GPAC,
		"MP4Box":     c.cfg.MP4Box,
		"mp4extract": c.cfg.MP4Extract,
		"mp4edit":    c.cfg.MP4Edit,
	}
	out := make([]domain.Capability, 0, len(tools))
	for name, binary := range tools {
		path, err := exec.LookPath(binary)
		cap := domain.Capability{Name: name, Available: err == nil, Path: path}
		if err != nil {
			cap.Error = err.Error()
		}
		out = append(out, cap)
	}
	return out
}

func (c *ToolChecker) Require(ctx context.Context) error {
	for _, cap := range c.Check(ctx) {
		if !cap.Available {
			return &MissingToolError{Name: cap.Name, Err: cap.Error}
		}
	}
	return nil
}

type MissingToolError struct {
	Name string
	Err  string
}

func (e *MissingToolError) Error() string {
	return "missing required media tool: " + e.Name + " (" + e.Err + ")"
}
