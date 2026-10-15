//go:build !cloud

package config

// Self-hosted default: the local gateway (deploy/oss binds 127.0.0.1:8080).
const defaultPublicBaseURL = "http://127.0.0.1:8080"

// Self-hosted builds send no notification mail unless NOTIFY_EMAIL_TO / NOTIFY_EMAIL_FROM are set.
const (
	defaultNotifyEmailTo   = ""
	defaultNotifyEmailFrom = ""
)
