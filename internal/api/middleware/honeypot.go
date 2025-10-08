package middleware

import (
	"net/http"

	"github.com/ivangsm/imagine/internal/database"
	"github.com/sirupsen/logrus"
)

const (
	// BanThreshold is the number of failed attempts before an IP is banned.
	BanThreshold = 3
)

// Honeypot is a middleware to detect and ban malicious actors.
type Honeypot struct {
	db     *database.HoneypotDB
	logger *logrus.Logger
}

// NewHoneypot creates a new Honeypot middleware.
func NewHoneypot(db *database.HoneypotDB, logger *logrus.Logger) *Honeypot {
	return &Honeypot{
		db:     db,
		logger: logger,
	}
}

// Handler is the main middleware handler.
// It checks if an IP is banned before allowing the request to proceed.
func (h *Honeypot) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := getClientIP(r)

		isBanned, err := h.db.IsBanned(clientIP, BanThreshold)
		if err != nil {
			h.logger.WithError(err).WithField("ip", clientIP).Error("Failed to check if IP is banned")
			// Fail open: allow request if DB check fails to avoid blocking legitimate users.
			next.ServeHTTP(w, r)
			return
		}

		if isBanned {
			h.logger.WithFields(logrus.Fields{
				"ip":         clientIP,
				"user_agent": r.Header.Get("User-Agent"),
				"path":       r.URL.Path,
			}).Warn("Blocked request from banned IP")

			// Return a 403 Forbidden status to indicate that the server is refusing to fulfill the request.
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RecordFailedAttempt is a helper to be called from other parts of the application (e.g., NotFound handler, auth middleware).
func (h *Honeypot) RecordFailedAttempt(r *http.Request) {
	clientIP := getClientIP(r)

	newAttemptCount, err := h.db.RecordFailedAttempt(clientIP)
	if err != nil {
		h.logger.WithError(err).WithField("ip", clientIP).Error("Failed to record failed attempt")
		return
	}

	h.logger.WithFields(logrus.Fields{
		"ip":        clientIP,
		"path":      r.URL.Path,
		"new_count": newAttemptCount,
		"threshold": BanThreshold,
	}).Info("Recorded failed access attempt")

	// Check if the new attempt count reaches the ban threshold and log the ban
	if newAttemptCount >= BanThreshold {
		// The IsBanned function will handle the actual banning, but we log it here for immediate feedback.
		h.logger.WithFields(logrus.Fields{
			"ip":        clientIP,
			"threshold": BanThreshold,
		}).Warn("IP banned after reaching threshold")
	}
}
