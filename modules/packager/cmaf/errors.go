package cmaf

import (
	"errors"
	"fmt"
)

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

func Unsupported(err error) (*UnsupportedError, bool) {
	var u *UnsupportedError
	if errors.As(err, &u) {
		return u, true
	}
	return nil, false
}
