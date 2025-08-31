package protocol

// Negotiation codes and welcome keys shared between client and server
const (
	CodeUnsupportedCodec      = "unsupported_codec"
	CodeBitrateOutOfRange     = "bitrate_out_of_range"
	CodeChannelsOutOfRange    = "channels_out_of_range"
	CodeSampleRateUnsupported = "sample_rate_unsupported"
	CodeWebBitrateLimit       = "web_bitrate_limit"
	CodeWebChannelsLimit      = "web_channels_limit"
	CodeInvalidUsername       = "invalid_username"
	CodeInvalidChannel        = "invalid_channel"
)

const (
	WelcomeKeyNegotiatedTransmitFormat = "negotiated_transmit_format"
	WelcomeKeyNegotiationAdjustments   = "negotiation_adjustments"
)
