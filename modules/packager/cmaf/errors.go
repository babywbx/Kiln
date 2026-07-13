package cmaf

import (
	"errors"
	"fmt"
)

// Reason values are stable identifiers surfaced as fallback_reason.
const (
	ReasonNotFragmented    = "not_fragmented_mp4"
	ReasonMultiTrackInit   = "multi_track_init"
	ReasonNoTrack          = "no_track"
	ReasonScheme           = "encryption_scheme_unsupported"
	ReasonCodec            = "codec_unsupported"
	ReasonInbandParamSets  = "inband_parameter_sets"
	ReasonMissingKID       = "missing_track_kid"
	ReasonMissingKey       = "missing_key_for_kid"
	ReasonSampleEntry      = "sample_entry_unsupported"
	ReasonHandler          = "handler_unsupported"
	ReasonMultiSampleEntry = "multi_sample_entry"
	ReasonMalformed        = "malformed_media"
)

// UnsupportedError marks input the native path cannot handle. Whether it may
// fall back to ffmpeg is decided by the planner, not here.
type UnsupportedError struct {
	Reason string
	Detail string
}

func (e *UnsupportedError) Error() string {
	if e.Detail == "" {
		return e.Reason
	}
	return e.Reason + ": " + e.Detail
}

func unsupportedf(reason, format string, args ...any) error {
	return &UnsupportedError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// Unsupported reports whether err means "native cannot handle this input",
// as opposed to a malformed or corrupt stream.
func Unsupported(err error) (*UnsupportedError, bool) {
	var u *UnsupportedError
	if errors.As(err, &u) {
		return u, true
	}
	return nil, false
}
