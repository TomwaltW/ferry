package fsp

import "errors"

var (
	ErrUnsupported     = errors.New("ferry: unsupported")
	ErrBusy            = errors.New("ferry: session busy")
	ErrNotFound        = errors.New("ferry: not found")
	ErrSessionExpired  = errors.New("ferry: platform session expired")
	ErrNoWindow        = errors.New("ferry: no reply window")
	ErrQuotaExceeded   = errors.New("ferry: quota exceeded")
	ErrWorkspaceDenied = errors.New("ferry: path outside workspace roots")
	ErrLocked          = errors.New("ferry: session held by another process")
)
