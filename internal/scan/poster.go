package scan

import (
	"context"
	"errors"
	"os"

	"github.com/cybersouris/tomovee/internal/thumbnail"
)

// ensure_frame_poster gives a catalog entry a locally extracted frame poster
// when it has no online artwork, so unmatched items are not left blank in the
// UI. It is best-effort: failures are logged and never fail the scan.
func (s *Scanner) ensure_frame_poster(ctx context.Context, entry_id int64, video_path string, duration_seconds float64) {
	if s.opts.Poster_dir == "" || s.extract == nil {
		return
	}
	entry, err := s.store.Get_catalog_entry(ctx, entry_id)
	if err != nil {
		s.opts.Logger.Debug("poster: load entry failed", "entry", entry_id, "error", err)
		return
	}
	if entry == nil || entry.Poster_path != "" {
		return
	}

	local := thumbnail.Local_path(s.opts.Poster_dir, entry_id)
	if info, err := os.Stat(local); err != nil || info.IsDir() {
		if err := s.extract(ctx, video_path, duration_seconds, local); err != nil {
			if errors.Is(err, thumbnail.Err_no_ffmpeg) {
				s.ffmpeg_warn.Do(func() {
					s.opts.Logger.Warn("frame posters disabled: ffmpeg not found on PATH")
				})
			} else {
				s.opts.Logger.Debug("frame poster extraction failed", "path", video_path, "error", err)
			}
			return
		}
	}

	entry.Poster_path = thumbnail.Local_marker
	if err := s.store.Update_catalog_entry(ctx, entry_id, *entry); err != nil {
		s.opts.Logger.Debug("poster: save entry failed", "entry", entry_id, "error", err)
	}
}
