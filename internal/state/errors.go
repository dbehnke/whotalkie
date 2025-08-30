package state

import "errors"

var (
	ErrUserNotFound      = errors.New("user not found")
	ErrChannelNotFound   = errors.New("channel not found")
	ErrUserAlreadyExists = errors.New("user already exists")
	ErrChannelFull       = errors.New("channel is full")
	ErrInvalidUserID     = errors.New("invalid user ID")
	ErrInvalidChannelID  = errors.New("invalid channel ID")
)
