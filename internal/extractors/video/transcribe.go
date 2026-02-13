package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/toricodesthings/file-processing-service/internal/extract"
	audioextractor "github.com/toricodesthings/file-processing-service/internal/extractors/audio"
)

type Extractor struct {
	ffmpegBinary string
	ffmpegTO     time.Duration
	audio        *audioextractor.Extractor
	maxBytes     int64
}

func New(ffmpegBinary string, ffmpegTimeout time.Duration, audio *audioextractor.Extractor, maxBytes int64) *Extractor {
	if strings.TrimSpace(ffmpegBinary) == "" {
		ffmpegBinary = "ffmpeg"
	}
	if ffmpegTimeout <= 0 {
		ffmpegTimeout = 120 * time.Second
	}
	return &Extractor{ffmpegBinary: ffmpegBinary, ffmpegTO: ffmpegTimeout, audio: audio, maxBytes: maxBytes}
}

func (e *Extractor) Name() string       { return "media/video" }
func (e *Extractor) MaxFileSize() int64 { return e.maxBytes }
func (e *Extractor) SupportedTypes() []string {
	return []string{"video/mp4", "video/x-matroska", "video/x-msvideo", "video/quicktime", "video/webm", "video/x-flv", "video/x-ms-wmv"}
}
func (e *Extractor) SupportedExtensions() []string {
	return []string{".mp4", ".mkv", ".avi", ".mov", ".webm", ".m4v", ".flv", ".wmv"}
}

func (e *Extractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
	if e.audio == nil {
		msg := "audio extractor dependency is nil"
		return extract.Result{Success: false, Method: "ffmpeg+groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, errors.New(msg)
	}

	outAudio := filepath.Join(filepath.Dir(job.LocalPath), "extracted.mp3")
	localCtx, cancel := context.WithTimeout(ctx, e.ffmpegTO)
	defer cancel()

	cmd := exec.CommandContext(localCtx, e.ffmpegBinary, "-y", "-i", job.LocalPath, "-vn", "-acodec", "mp3", "-ab", "128k", outAudio)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := fmt.Sprintf("ffmpeg failed: %v: %s", err, strings.TrimSpace(string(out)))
		return extract.Result{Success: false, Method: "ffmpeg+groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	st, err := os.Stat(outAudio)
	if err != nil {
		msg := fmt.Sprintf("ffmpeg output missing: %v", err)
		return extract.Result{Success: false, Method: "ffmpeg+groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}
	if st.Size() <= 0 {
		msg := "ffmpeg produced empty audio track"
		return extract.Result{Success: false, Method: "ffmpeg+groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, errors.New(msg)
	}

	audioJob := job
	audioJob.LocalPath = outAudio
	audioJob.MIMEType = "audio/mpeg"
	audioJob.FileSize = st.Size()
	res, err := e.audio.Extract(ctx, audioJob)
	if err != nil {
		return res, err
	}
	res.Method = "ffmpeg+" + res.Method
	res.FileType = e.Name()
	return res, nil
}
