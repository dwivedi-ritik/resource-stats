package collector

import (
	"errors"
	"io/fs"
	"strings"
	"syscall"
)

// humanDockerError converts a transport error into a sentence a person can
// act on. The original error text is appended so nothing is hidden.
func humanDockerError(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOENT):
		return "Docker socket not found. Is Docker running? (" + err.Error() + ")"
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES):
		return "No permission to read the Docker socket. Add this user to the docker group or mount the socket. (" + err.Error() + ")"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "Docker refused the connection. Is the daemon running? (" + err.Error() + ")"
	case strings.Contains(err.Error(), "context deadline exceeded"):
		return "Docker did not answer in time. (" + err.Error() + ")"
	}
	return "Could not reach Docker: " + err.Error()
}
