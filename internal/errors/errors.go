package errors

import (
	"context"
	stderrors "errors"
	"net/http"

	"opencoderouter/internal/session"
)

var (
	ErrSessionNotFound = stderrors.New("session not found")
	ErrDaemonUnhealthy = stderrors.New("daemon unhealthy")
	ErrAuthFailed      = stderrors.New("authentication failed")
	ErrPortExhausted   = stderrors.New("no available ports")
)

func HTTPStatus(err error) int {
	switch {
	case isSessionNotFound(err):
		return http.StatusNotFound
	case stderrors.Is(err, session.ErrWorkspacePathRequired), stderrors.Is(err, session.ErrWorkspacePathInvalid):
		return http.StatusBadRequest
	case stderrors.Is(err, session.ErrSessionAlreadyExists), stderrors.Is(err, session.ErrSessionStopped):
		return http.StatusConflict
	case isPortExhausted(err):
		return http.StatusServiceUnavailable
	case stderrors.Is(err, ErrAuthFailed):
		return http.StatusUnauthorized
	case stderrors.Is(err, ErrDaemonUnhealthy), stderrors.Is(err, session.ErrTerminalAttachDisabled):
		return http.StatusServiceUnavailable
	case stderrors.Is(err, context.Canceled):
		return http.StatusRequestTimeout
	case stderrors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

func Code(err error) string {
	switch {
	case isSessionNotFound(err):
		return "SESSION_NOT_FOUND"
	case stderrors.Is(err, session.ErrWorkspacePathRequired):
		return "WORKSPACE_PATH_REQUIRED"
	case stderrors.Is(err, session.ErrWorkspacePathInvalid):
		return "WORKSPACE_PATH_INVALID"
	case stderrors.Is(err, session.ErrSessionAlreadyExists):
		return "SESSION_ALREADY_EXISTS"
	case isPortExhausted(err):
		return "NO_AVAILABLE_SESSION_PORTS"
	case stderrors.Is(err, session.ErrSessionStopped):
		return "SESSION_STOPPED"
	case stderrors.Is(err, ErrAuthFailed):
		return "AUTH_FAILED"
	case stderrors.Is(err, ErrDaemonUnhealthy):
		return "DAEMON_UNHEALTHY"
	case stderrors.Is(err, session.ErrTerminalAttachDisabled):
		return "TERMINAL_ATTACH_UNAVAILABLE"
	case stderrors.Is(err, context.Canceled):
		return "REQUEST_CANCELED"
	case stderrors.Is(err, context.DeadlineExceeded):
		return "REQUEST_TIMEOUT"
	default:
		return "INTERNAL_ERROR"
	}
}

func Message(err error) string {
	switch {
	case isSessionNotFound(err):
		return "session not found"
	case stderrors.Is(err, session.ErrWorkspacePathRequired):
		return "workspace path is required"
	case stderrors.Is(err, session.ErrWorkspacePathInvalid):
		return "workspace path is invalid"
	case stderrors.Is(err, session.ErrSessionAlreadyExists):
		return "session already exists"
	case isPortExhausted(err):
		return "no available session ports"
	case stderrors.Is(err, session.ErrSessionStopped):
		return "session is stopped"
	case stderrors.Is(err, ErrAuthFailed):
		return "authentication failed"
	case stderrors.Is(err, ErrDaemonUnhealthy):
		return "daemon unhealthy"
	case stderrors.Is(err, session.ErrTerminalAttachDisabled):
		return "terminal attachment is unavailable"
	case stderrors.Is(err, context.Canceled):
		return "request canceled"
	case stderrors.Is(err, context.DeadlineExceeded):
		return "request timeout"
	default:
		return "internal server error"
	}
}

func isSessionNotFound(err error) bool {
	return stderrors.Is(err, ErrSessionNotFound) || stderrors.Is(err, session.ErrSessionNotFound)
}

func isPortExhausted(err error) bool {
	return stderrors.Is(err, ErrPortExhausted) || stderrors.Is(err, session.ErrNoAvailableSessionPorts)
}
